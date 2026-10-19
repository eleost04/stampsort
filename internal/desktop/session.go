package desktop

import (
	"bytes"
	"crypto/sha256"
	"fmt"
	"image"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"sync"
	"time"

	"github.com/eleost04/stampsort/internal/imgx"
	"github.com/eleost04/stampsort/internal/input"
	"github.com/eleost04/stampsort/internal/jpegenc"
	"github.com/eleost04/stampsort/internal/photo"
	"github.com/eleost04/stampsort/internal/print6"
	"github.com/eleost04/stampsort/internal/stamp"
)

// Item 是工作区里的一张照片。导出给前端的字段带 json 标签。
type Item struct {
	ID     int    `json:"id"`
	Name   string `json:"name"`
	Status string `json:"status"` // pending | ready | error
	Time   string `json:"time,omitempty"`
	Clock  bool   `json:"clock"`
	Source string `json:"source,omitempty"`
	Raw    string `json:"raw,omitempty"`
	W      int    `json:"w,omitempty"`
	H      int    `json:"h,omitempty"`
	Err    string `json:"err,omitempty"`

	path     string
	hash     [32]byte
	detected stamp.Result
	manual   *stamp.Result
	thumb    []byte
	preview  []byte // 6 寸效果图缓存
	seq      int    // 加入顺序
	retried  bool   // 已经用学到的水印位置重认过
}

// effective 是导出时使用的时间：手动填写的优先。
func (it *Item) effective() stamp.Result {
	if it.manual != nil {
		return *it.manual
	}
	return it.detected
}

func (it *Item) refresh() {
	r := it.effective()
	it.Source = string(r.Source)
	it.Clock = r.HasClock
	it.Time = ""
	if r.Source != stamp.None && r.Source != "" {
		it.Time = r.Time.Format("2006-01-02 15:04")
		if !r.HasClock {
			it.Time = r.Time.Format("2006-01-02")
		}
	}
}

// Session 管理一个窗口里的全部照片和后台识别。
type Session struct {
	det    *stamp.Detector
	tmp    string // 上传、解压用的临时目录
	notify func()
	queue  chan *Item
	gate   *gate

	mu      sync.Mutex
	items   []*Item
	byID    map[int]*Item
	byHash  map[[32]byte]*Item
	nextID  int
	pending int
	cached  []*Item // 有 6 寸预览缓存的，超过上限时释放最早的

	batchStart time.Time // 这一批开始识别的时间，用来算速度
	batchDone  int
	previewGen int // 排版参数每改一次加一，旧参数算出的预览不再缓存
}

// gate 是可以随时调整上限的并发闸门：设置里改"同时识别张数"立即生效。
type gate struct {
	mu            sync.Mutex
	cond          *sync.Cond
	limit, active int
}

func newGate(n int) *gate {
	g := &gate{limit: n}
	g.cond = sync.NewCond(&g.mu)
	return g
}

func (g *gate) enter() {
	g.mu.Lock()
	for g.active >= g.limit {
		g.cond.Wait()
	}
	g.active++
	g.mu.Unlock()
}

func (g *gate) leave() {
	g.mu.Lock()
	g.active--
	g.mu.Unlock()
	g.cond.Signal()
}

func (g *gate) set(n int) {
	g.mu.Lock()
	g.limit = max(1, n)
	g.mu.Unlock()
	g.cond.Broadcast()
}

// maxWorkers 是工作 goroutine 的数量上限，实际并发由 gate 控制。
const maxWorkers = 32

// Workers 把 0 解释为 CPU 核数。
func Workers(n int) int {
	if n <= 0 {
		return runtime.NumCPU()
	}
	return min(n, maxWorkers)
}

const maxPreviews = 24

// NewSession 创建会话并启动识别工作池。notify 在状态变化时调用（会被频繁调用，应自行节流）。
func NewSession(det *stamp.Detector, tmp string, notify func()) *Session {
	s := &Session{det: det, tmp: tmp, notify: notify, queue: make(chan *Item, 1<<16),
		gate: newGate(runtime.NumCPU()), byID: map[int]*Item{}, byHash: map[[32]byte]*Item{}}
	for range maxWorkers {
		go s.worker()
	}
	go s.heartbeat()
	return s
}

// heartbeat 定时刷新临时目录的修改时间，表明它还在用：别的实例启动时只清理一天没刷新过的。
func (s *Session) heartbeat() {
	for {
		now := time.Now()
		if os.Chtimes(s.tmp, now, now) != nil {
			return // 目录已删（会话结束）
		}
		time.Sleep(30 * time.Minute)
	}
}

// SetWorkers 调整同时识别的张数（0 = CPU 核数）。
func (s *Session) SetWorkers(n int) { s.gate.set(Workers(n)) }

// AddResult 汇报一次加入的结果。
type AddResult struct {
	Added    int      `json:"added"`
	Dup      int      `json:"dup"`
	Warnings []string `json:"warnings,omitempty"`
}

// AddPaths 加入文件、目录或 zip（zip 解压到临时目录）。names 不为空时作为显示名（上传的文件用原文件名）。
func (s *Session) AddPaths(paths []string) (AddResult, error) {
	items, warns, err := input.Collect(paths, s.tmp, input.DefaultLimits)
	if err != nil {
		return AddResult{Warnings: warns}, err
	}
	res := AddResult{Warnings: warns}
	var fresh []*Item
	for _, in := range items {
		h, err := hashFile(in.Path)
		if err != nil {
			res.Warnings = append(res.Warnings, fmt.Sprintf("%s: %v", in.Name, err))
			continue
		}
		s.mu.Lock()
		if s.byHash[h] != nil {
			s.mu.Unlock()
			res.Dup++
			continue
		}
		if s.pending == 0 {
			s.batchStart, s.batchDone = time.Now(), 0
		}
		s.nextID++
		it := &Item{ID: s.nextID, Name: in.Name, Status: "pending", path: in.Path, hash: h, seq: s.nextID}
		s.items = append(s.items, it)
		s.byID[it.ID] = it
		s.byHash[h] = it
		s.pending++
		s.mu.Unlock()
		fresh = append(fresh, it)
	}
	res.Added = len(fresh)
	if len(fresh) > 0 {
		go func() {
			for _, it := range fresh {
				s.queue <- it
			}
		}()
		s.notify()
	}
	return res, nil
}

func hashFile(p string) ([32]byte, error) {
	f, err := os.Open(p)
	if err != nil {
		return [32]byte{}, err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return [32]byte{}, err
	}
	var out [32]byte
	copy(out[:], h.Sum(nil))
	return out, nil
}

func (s *Session) worker() {
	for it := range s.queue {
		s.mu.Lock()
		gone := s.byID[it.ID] == nil
		s.mu.Unlock()
		if gone { // 识别前已被删除
			continue
		}
		s.gate.enter()
		s.recognize(it)
		s.gate.leave()
	}
}

// recognize 解码、识别时间、生成缩略图。
func (s *Session) recognize(it *Item) {
	img, meta, err := photo.Load(it.path)
	var res stamp.Result
	var thumb []byte
	if err == nil {
		gray := imgx.Luma(img)
		res = s.det.Detect(img, gray, meta, it.Name)
		thumb = encodeThumb(img, 360)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.byID[it.ID] == nil {
		return
	}
	s.pending--
	s.batchDone++
	if err != nil {
		it.Status, it.Err = "error", "打不开: "+err.Error()
	} else {
		it.Status = "ready"
		// 重认时只在结果更可靠（读到了水印）或原来什么都没有时才替换
		if it.thumb == nil || res.Source == stamp.FromOCR || it.detected.Source == stamp.None {
			it.detected = res
			it.Raw = res.Raw
		}
		it.W, it.H = img.Rect.Dx(), img.Rect.Dy()
		it.thumb = thumb
		it.refresh()
	}
	if s.pending == 0 {
		s.retryLocked()
	}
	go s.notify()
}

// retryLocked：一批识别完后，如果已经学到了水印位置，把没读到水印的照片
// （未识别、或只从文件名/EXIF 得到时间的）按位置再认一次。每张最多自动重认一次。
func (s *Session) retryLocked() {
	if s.det.OCR == nil {
		return
	}
	var again []*Item
	for _, it := range s.items {
		if it.Status != "ready" || it.manual != nil || it.retried || it.detected.Source == stamp.FromOCR || it.detected.Source == stamp.FromMark {
			continue
		}
		if _, ok := s.det.HintFor(it.H > it.W); !ok {
			continue
		}
		it.retried = true
		it.Status = "pending"
		s.pending++
		again = append(again, it)
	}
	if len(again) > 0 {
		go func() {
			for _, it := range again {
				s.queue <- it
			}
		}()
	}
}

// Redetect 手动重新识别一张（撤销手动填写的时间）。
func (s *Session) Redetect(id int) error {
	s.mu.Lock()
	it := s.byID[id]
	if it == nil || it.Status == "pending" {
		s.mu.Unlock()
		return os.ErrNotExist
	}
	it.retried = true
	it.manual = nil
	it.detected = stamp.Result{Source: stamp.None}
	it.Status, it.Err = "pending", ""
	if s.pending == 0 {
		s.batchStart, s.batchDone = time.Now(), 0
	}
	s.pending++
	s.mu.Unlock()
	s.queue <- it
	s.notify()
	return nil
}

func encodeThumb(img *image.RGBA, long int) []byte {
	w, h := img.Rect.Dx(), img.Rect.Dy()
	if w >= h && w > long {
		w, h = long, max(1, h*long/w)
	} else if h > w && h > long {
		w, h = max(1, w*long/h), long
	}
	small := imgx.Resize(img, w, h)
	var buf bytes.Buffer
	jpegenc.Encode(&buf, small, &jpegenc.Options{Quality: 82})
	return buf.Bytes()
}

// Snapshot 返回按显示顺序排好的照片列表、仍在识别的数量，以及这一批的识别速度（张/秒）。
func (s *Session) Snapshot() ([]Item, int, float64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Item, 0, len(s.items))
	for _, it := range s.ordered() {
		cp := *it
		out = append(out, cp)
	}
	var rate float64
	if el := time.Since(s.batchStart).Seconds(); s.pending > 0 && s.batchDone > 0 && el > 0.5 {
		rate = float64(s.batchDone) / el
	}
	return out, s.pending, rate
}

// ordered 是导出和显示共用的顺序：按时间升序，同时间按加入顺序；未识别和出错的放最后。
func (s *Session) ordered() []*Item {
	list := append([]*Item(nil), s.items...)
	sort.SliceStable(list, func(i, j int) bool {
		a, b := list[i].effective(), list[j].effective()
		ka, kb := a.Source == stamp.None || a.Source == "", b.Source == stamp.None || b.Source == ""
		if ka != kb {
			return !ka
		}
		if !ka && !a.Time.Equal(b.Time) {
			return a.Time.Before(b.Time)
		}
		return list[i].seq < list[j].seq
	})
	return list
}

// Get 返回一张照片（副本）和它的源文件路径。
func (s *Session) Get(id int) (*Item, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	it := s.byID[id]
	if it == nil {
		return nil, false
	}
	cp := *it
	return &cp, true
}

// Thumb 返回缩略图 JPEG。
func (s *Session) Thumb(id int) []byte {
	s.mu.Lock()
	defer s.mu.Unlock()
	if it := s.byID[id]; it != nil {
		return it.thumb
	}
	return nil
}

// Preview 生成（并缓存）6 寸效果图。
func (s *Session) Preview(id int, layout print6.Options) ([]byte, error) {
	s.mu.Lock()
	it := s.byID[id]
	if it == nil {
		s.mu.Unlock()
		return nil, os.ErrNotExist
	}
	if it.preview != nil {
		p := it.preview
		s.mu.Unlock()
		return p, nil
	}
	path, gen := it.path, s.previewGen
	s.mu.Unlock()

	img, _, err := photo.Load(path)
	if err != nil {
		return nil, err
	}
	out, _ := print6.Make(img, nil, layout)
	var buf bytes.Buffer
	if err := jpegenc.Encode(&buf, out, &jpegenc.Options{Quality: 85}); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.byID[id] != nil && gen == s.previewGen { // 计算期间改了排版参数就不缓存
		it.preview = buf.Bytes()
		s.cached = append(s.cached, it)
		if len(s.cached) > maxPreviews {
			s.cached[0].preview = nil
			s.cached = s.cached[1:]
		}
	}
	return buf.Bytes(), nil
}

// DropPreviews 在排版参数改变后清空 6 寸效果缓存。
func (s *Session) DropPreviews() {
	s.mu.Lock()
	for _, it := range s.cached {
		it.preview = nil
	}
	s.cached = nil
	s.previewGen++
	s.mu.Unlock()
}

// SetTime 手动设置时间。t 为空字符串表示撤销手动修改，恢复识别结果。
func (s *Session) SetTime(id int, t string) error {
	var manual *stamp.Result
	if t != "" {
		tm, clock, err := parseManual(t)
		if err != nil {
			return err
		}
		manual = &stamp.Result{Time: tm, HasClock: clock, Source: stamp.Manual}
	}
	s.mu.Lock()
	it := s.byID[id]
	if it == nil {
		s.mu.Unlock()
		return os.ErrNotExist
	}
	it.manual = manual
	it.refresh()
	s.mu.Unlock()
	s.notify()
	return nil
}

// parseManual 接受 "2026-08-13 06:20"、"2026-08-13T06:20"、"2026-08-13"。
func parseManual(t string) (time.Time, bool, error) {
	for _, f := range []string{"2006-01-02 15:04", "2006-01-02T15:04", "2006-01-02 15:04:05", "2006-01-02T15:04:05"} {
		if tm, err := time.ParseInLocation(f, t, time.Local); err == nil {
			return tm, true, nil
		}
	}
	if tm, err := time.ParseInLocation("2006-01-02", t, time.Local); err == nil {
		return tm, false, nil
	}
	return time.Time{}, false, fmt.Errorf("时间格式不对：%q", t)
}

// Remove 删除照片；ids 为空时清空全部。
func (s *Session) Remove(ids []int) {
	s.mu.Lock()
	if len(ids) == 0 {
		for _, it := range s.items {
			ids = append(ids, it.ID)
		}
	}
	drop := map[int]bool{}
	for _, id := range ids {
		if it := s.byID[id]; it != nil {
			drop[id] = true
			delete(s.byID, id)
			delete(s.byHash, it.hash)
			if it.Status == "pending" {
				s.pending--
			}
		}
	}
	kept := s.items[:0]
	for _, it := range s.items {
		if !drop[it.ID] {
			kept = append(kept, it)
		}
	}
	s.items = kept
	s.mu.Unlock()
	s.notify()
}

// ExportList 返回可导出的照片（已识别完、没出错）和对应的时间预设，顺序与界面一致。
func (s *Session) ExportList() ([]input.Item, []*stamp.Result, int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var items []input.Item
	var presets []*stamp.Result
	skipped := 0
	for _, it := range s.ordered() {
		if it.Status != "ready" {
			skipped++
			continue
		}
		r := it.effective()
		items = append(items, input.Item{Path: it.path, Name: it.Name})
		presets = append(presets, &r)
	}
	return items, presets, skipped
}

// TempDir 返回会话临时目录下一个新的子目录，供上传使用。
func (s *Session) TempDir(prefix string) (string, error) {
	return os.MkdirTemp(s.tmp, prefix)
}

// Close 停止接收任务并删除临时文件。
func (s *Session) Close() {
	os.RemoveAll(s.tmp)
}

// safeJoin 把名字限制在 dir 内。
func safeJoin(dir, name string) string {
	return filepath.Join(dir, filepath.Base(filepath.Clean("/"+name)))
}
