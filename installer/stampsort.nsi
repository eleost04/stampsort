; stampsort 安装程序（NSIS 3）
;
; - 按当前用户安装到 %LOCALAPPDATA%\Programs\stampsort，不需要管理员权限，不弹 UAC
; - 开始菜单 / 桌面快捷方式，右键「发送到」里加 stampsort（选中照片或文件夹直接加入）
; - 卸载信息写在 HKCU，可在"应用和功能"里卸载
; - 支持 /S 静默安装（自动更新用）；/relaunch 装完后重新打开程序
;
; 构建（Makefile 的 installer 目标）：
;   makensis -DVERSION=0.4.0 -DSRC=../dist/win -DOUTFILE=../dist/stampsort-setup-v0.4.0.exe installer/stampsort.nsi

Unicode true
ManifestDPIAware true
!include "MUI2.nsh"
!include "LogicLib.nsh"
!include "FileFunc.nsh"

!ifndef VERSION
  !define VERSION "0.0.0"
!endif
!ifndef SRC
  !define SRC "..\dist\win"
!endif
!ifndef OUTFILE
  !define OUTFILE "..\dist\stampsort-setup.exe"
!endif

!define DISPLAYNAME "stampsort 水印照片整理"
!define UNINSTKEY "Software\Microsoft\Windows\CurrentVersion\Uninstall\stampsort"
!define LNKNAME "stampsort 水印照片整理.lnk"

Name "${DISPLAYNAME}"
OutFile "${OUTFILE}"
RequestExecutionLevel user
InstallDir "$LOCALAPPDATA\Programs\stampsort"
InstallDirRegKey HKCU "${UNINSTKEY}" "InstallLocation"
SetCompressor /SOLID lzma
BrandingText "stampsort ${VERSION}"

VIProductVersion "${VERSION}.0"
VIAddVersionKey /LANG=2052 "ProductName" "stampsort"
VIAddVersionKey /LANG=2052 "FileDescription" "${DISPLAYNAME} 安装程序"
VIAddVersionKey /LANG=2052 "FileVersion" "${VERSION}"
VIAddVersionKey /LANG=2052 "ProductVersion" "${VERSION}"
VIAddVersionKey /LANG=2052 "LegalCopyright" "MIT License"

!define MUI_ICON "..\winres\icon.ico"
!define MUI_UNICON "..\winres\icon.ico"
!define MUI_ABORTWARNING
!define MUI_WELCOMEPAGE_TEXT "将安装 ${DISPLAYNAME} ${VERSION}。$\r$\n$\r$\n只为当前用户安装，不需要管理员权限。$\r$\n$\r$\n安装后可以在右键菜单「发送到」里找到 stampsort，选中照片或文件夹直接发送给它。"
!define MUI_FINISHPAGE_RUN "$INSTDIR\stampsort.exe"
!define MUI_FINISHPAGE_RUN_TEXT "打开 stampsort"

!insertmacro MUI_PAGE_WELCOME
!insertmacro MUI_PAGE_DIRECTORY
!insertmacro MUI_PAGE_INSTFILES
!insertmacro MUI_PAGE_FINISH
!insertmacro MUI_UNPAGE_CONFIRM
!insertmacro MUI_UNPAGE_INSTFILES
!insertmacro MUI_LANGUAGE "SimpChinese"

Var Relaunch

Function .onInit
  StrCpy $Relaunch 0
  ${GetParameters} $0
  ClearErrors
  ${GetOptions} $0 "/relaunch" $1
  ${IfNot} ${Errors}
    StrCpy $Relaunch 1
  ${EndIf}
FunctionEnd

; 程序在运行时 exe 删不掉：静默安装（自动更新）时等它退出，最多 30 秒；手动安装时提示关闭。
!macro WaitForApp un
Function ${un}WaitForApp
  StrCpy $R0 0
  retry:
    IfFileExists "$INSTDIR\stampsort.exe" 0 done
    ClearErrors
    Delete "$INSTDIR\stampsort.exe"
    IfErrors 0 done
    IntOp $R0 $R0 + 1
    ${If} ${Silent}
      ${If} $R0 > 60
        ; 自动更新时旧版迟迟不退出：放弃这次更新，把旧版重新打开，别让用户的窗口凭空消失
        ${If} $Relaunch == 1
          Exec '"$INSTDIR\stampsort.exe"'
        ${EndIf}
        Abort
      ${EndIf}
      Sleep 500
      Goto retry
    ${EndIf}
    MessageBox MB_RETRYCANCEL|MB_ICONEXCLAMATION "stampsort 正在运行，请先关闭它，再点「重试」。" IDRETRY retry
    Abort
  done:
FunctionEnd
!macroend
!insertmacro WaitForApp ""
!insertmacro WaitForApp "un."

Section "stampsort"
  SetOutPath "$INSTDIR"
  Call WaitForApp
  File "${SRC}\stampsort.exe"
  File "${SRC}\onnxruntime.dll"
  File "${SRC}\msvcp140.dll"
  File "${SRC}\msvcp140_1.dll"
  File "${SRC}\vcruntime140.dll"
  File "${SRC}\vcruntime140_1.dll"
  File "${SRC}\README.md"
  WriteUninstaller "$INSTDIR\uninstall.exe"

  CreateShortcut "$SMPROGRAMS\${LNKNAME}" "$INSTDIR\stampsort.exe"
  CreateShortcut "$SENDTO\${LNKNAME}" "$INSTDIR\stampsort.exe"
  ${IfNot} ${Silent} ; 自动更新时不重建桌面快捷方式（用户可能删过）
    CreateShortcut "$DESKTOP\${LNKNAME}" "$INSTDIR\stampsort.exe"
  ${EndIf}

  WriteRegStr HKCU "${UNINSTKEY}" "DisplayName" "${DISPLAYNAME}"
  WriteRegStr HKCU "${UNINSTKEY}" "DisplayVersion" "${VERSION}"
  WriteRegStr HKCU "${UNINSTKEY}" "Publisher" "stampsort"
  WriteRegStr HKCU "${UNINSTKEY}" "DisplayIcon" "$INSTDIR\stampsort.exe"
  WriteRegStr HKCU "${UNINSTKEY}" "InstallLocation" "$INSTDIR"
  WriteRegStr HKCU "${UNINSTKEY}" "UninstallString" '"$INSTDIR\uninstall.exe"'
  WriteRegStr HKCU "${UNINSTKEY}" "QuietUninstallString" '"$INSTDIR\uninstall.exe" /S'
  WriteRegDWORD HKCU "${UNINSTKEY}" "NoModify" 1
  WriteRegDWORD HKCU "${UNINSTKEY}" "NoRepair" 1
  ${GetSize} "$INSTDIR" "/S=0K" $0 $1 $2
  WriteRegDWORD HKCU "${UNINSTKEY}" "EstimatedSize" $0

  ${If} $Relaunch == 1
    Exec '"$INSTDIR\stampsort.exe"'
  ${EndIf}
SectionEnd

Section "Uninstall"
  Call un.WaitForApp
  Delete "$INSTDIR\onnxruntime.dll"
  Delete "$INSTDIR\msvcp140.dll"
  Delete "$INSTDIR\msvcp140_1.dll"
  Delete "$INSTDIR\vcruntime140.dll"
  Delete "$INSTDIR\vcruntime140_1.dll"
  Delete "$INSTDIR\README.md"
  Delete "$INSTDIR\uninstall.exe"
  RMDir "$INSTDIR"
  Delete "$SMPROGRAMS\${LNKNAME}"
  Delete "$SENDTO\${LNKNAME}"
  Delete "$DESKTOP\${LNKNAME}"
  DeleteRegKey HKCU "${UNINSTKEY}"
  RMDir /r "$LOCALAPPDATA\stampsort" ; 界面缓存
  ${IfNot} ${Silent}
    MessageBox MB_YESNO|MB_ICONQUESTION "是否同时删除设置和日志？$\r$\n（包括保存位置、分类方式、记住的水印位置）" IDNO keep
    RMDir /r "$APPDATA\stampsort"
    keep:
  ${EndIf}
SectionEnd
