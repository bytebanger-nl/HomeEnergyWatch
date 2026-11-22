; Inno Setup script for the hwlocal Windows installer.
;
; Build: installer\build.ps1 (or, with the exe in dist\:
;   ISCC /DAppVersion=1.2.3 installer\hwlocal.iss)
;
; Install: copies hwlocal.exe to Program Files\hwlocal and runs
; "hwlocal service install", which registers the service, prepares the data
; directory in ProgramData\hwlocal and adds the firewall rule. An update runs
; a newer setup: the service is stopped, the exe replaced and the service
; started with its stored flags; the database schema is migrated on start,
; after a backup (hwlocal.db.v<N>.bak).

#ifndef AppVersion
  #define AppVersion "0.0.0-dev"
#endif
#ifndef NumericVersion
  #define NumericVersion "0.0.0.0"
#endif
#ifndef SourceExe
  #define SourceExe "..\dist\hwlocal.exe"
#endif
; the name users see; the service, folders and exe stay "hwlocal" so that
; upgrades find the existing install
#define AppTitle "Home Energy Watch"

[Setup]
AppId={{A7BD9DC4-4600-46A8-ABD8-B4BF849F2E67}
AppName={#AppTitle}
AppVersion={#AppVersion}
AppVerName={#AppTitle} {#AppVersion}
; shown in Windows Apps and in the file properties of the setup
VersionInfoVersion={#NumericVersion}
VersionInfoProductVersion={#NumericVersion}
VersionInfoProductTextVersion={#AppVersion}
AppPublisher=bytebanger-nl
AppComments=Lokaal energiedashboard; werkt met HomeWizard Energy-apparaten (niet verbonden aan HomeWizard)
; the service code expects this location (hwlocal service install)
DefaultDirName={commonpf64}\hwlocal
DisableDirPage=yes
DefaultGroupName={#AppTitle}
; the group was "HomeWizard Local" before the rename: do not reuse it, an
; upgrade moves the old shortcut (MoveOldShortcut)
UsePreviousGroup=no
DisableProgramGroupPage=yes
PrivilegesRequired=admin
ArchitecturesAllowed=x64compatible
ArchitecturesInstallIn64BitMode=x64compatible
OutputDir=..\dist
OutputBaseFilename=hwlocal-setup-{#AppVersion}
Compression=lzma2
SolidCompression=yes
WizardStyle=modern
UninstallDisplayIcon={app}\hwlocal.exe
UninstallDisplayName={#AppTitle}
; the service is stopped in PrepareToInstall, not by Restart Manager
CloseApplications=no
SetupLogging=yes

[Languages]
Name: "nl"; MessagesFile: "compiler:Languages\Dutch.isl"

[Files]
Source: "{#SourceExe}"; DestDir: "{app}"; DestName: "hwlocal.exe"; Flags: ignoreversion

[INI]
Filename: "{group}\{#AppTitle}.url"; Section: "InternetShortcut"; Key: "URL"; String: "{code:AppURL}"; Check: IsNewInstall

[UninstallDelete]
Type: files; Name: "{group}\{#AppTitle}.url"
Type: files; Name: "{commonprograms}\HomeWizard Local\HomeWizard Local.url"
Type: dirifempty; Name: "{commonprograms}\HomeWizard Local"

[Run]
Filename: "{code:AppURL}"; Description: "{#AppTitle} openen in de browser"; Flags: postinstall shellexec nowait skipifsilent; Check: IsNewInstall

[Code]
var
  Upgrade: Boolean;
  SettingsPage: TInputQueryWizardPage;
  TokenPage: TInputQueryWizardPage;
  HttpsCheck: TNewCheckBox;

function InitializeSetup: Boolean;
begin
  // an existing service (from this installer or "hwlocal service install")
  // keeps its settings
  Upgrade := RegKeyExists(HKLM, 'SYSTEM\CurrentControlSet\Services\hwlocal');
  Result := True;
end;

function IsNewInstall: Boolean;
begin
  Result := not Upgrade;
end;

procedure InitializeWizard;
begin
  SettingsPage := CreateInputQueryPage(wpWelcome, 'Instellingen',
    'Hoe moet de gateway bereikbaar zijn?',
    'Later wijzigen kan met "hwlocal service install <vlaggen>" in een opdrachtprompt als administrator.');
  SettingsPage.Add('HTTP-poort:', False);
  SettingsPage.Values[0] := '8080';
  HttpsCheck := TNewCheckBox.Create(SettingsPage);
  HttpsCheck.Parent := SettingsPage.Surface;
  HttpsCheck.Top := SettingsPage.Edits[0].Top + SettingsPage.Edits[0].Height + ScaleY(16);
  HttpsCheck.Width := SettingsPage.SurfaceWidth;
  HttpsCheck.Height := ScaleY(17);
  HttpsCheck.Caption := 'Ook HTTPS met een eigen certificaat (poort 8443)';
  with TNewStaticText.Create(SettingsPage) do
  begin
    Parent := SettingsPage.Surface;
    Top := HttpsCheck.Top + HttpsCheck.Height + ScaleY(4);
    Left := ScaleX(18);
    Width := SettingsPage.SurfaceWidth - ScaleX(18);
    WordWrap := True;
    Caption := 'Nodig om de app op je telefoon te installeren. Elk apparaat moet dan eenmalig het certificaat van /ca.crt vertrouwen.';
  end;

  // only on a fresh install; later the token is set in the app
  TokenPage := CreateInputQueryPage(SettingsPage.ID, 'Stroomprijzen',
    'Optioneel: ENTSO-E als extra bron voor stroomprijzen',
    'EnergyZero en Nord Pool werken zonder account. ENTSO-E is een officiële, gratis reservebron; ' +
    'vraag een token aan via transparency.entsoe.eu. Je kunt het ook later in de app instellen (Apparaten, Prijsbronnen).');
  TokenPage.Add('ENTSO-E API-token:', True);
end;

// WriteToken stores the token entered in the wizard in the data directory,
// where the service reads it; the ACL of that directory (set by "service
// install") keeps it from other users.
procedure WriteToken;
var
  Token, Dir: String;
begin
  Token := Trim(TokenPage.Values[0]);
  if Token = '' then
    exit;
  Dir := ExpandConstant('{commonappdata}\hwlocal');
  ForceDirectories(Dir);
  if not SaveStringToFile(Dir + '\entsoe-token.txt', Token, False) then
    MsgBox('Het ENTSO-E-token kon niet worden opgeslagen in ' + Dir + '.', mbError, MB_OK)
  else
    Log('ENTSO-E token saved');
end;

function ShouldSkipPage(PageID: Integer): Boolean;
begin
  // an update keeps the settings and the token; the token can be changed in
  // the app (Apparaten, Prijsbronnen)
  Result := Upgrade and ((PageID = SettingsPage.ID) or (PageID = TokenPage.ID));
end;

// PortsFree asks the hwlocal.exe in this setup whether the ports can be
// listened on, the same way the service will; Problem gets its explanation.
function PortsFree(Ports: String; var Problem: String): Boolean;
var
  RC: Integer;
  Exe, OutFile: String;
  Output: AnsiString;
begin
  ExtractTemporaryFile('hwlocal.exe');
  Exe := ExpandConstant('{tmp}\hwlocal.exe');
  OutFile := ExpandConstant('{tmp}\check-port.txt');
  Result := Exec(ExpandConstant('{cmd}'),
    '/C ""' + Exe + '" service check-port ' + Ports + ' > "' + OutFile + '" 2>&1"',
    '', SW_HIDE, ewWaitUntilTerminated, RC) and (RC = 0);
  if not Result then
  begin
    Output := '';
    LoadStringFromFile(OutFile, Output);
    Problem := Trim(String(Output));
    if Problem = '' then
      Problem := 'De poortcontrole kon niet worden uitgevoerd.';
  end;
end;

function ChosenPorts: String;
begin
  Result := ':' + Trim(SettingsPage.Values[0]);
  if HttpsCheck.Checked then
    Result := Result + ' :8443';
end;

function NextButtonClick(CurPageID: Integer): Boolean;
var
  Port: Integer;
  Problem: String;
begin
  Result := True;
  if CurPageID = SettingsPage.ID then
  begin
    Port := StrToIntDef(Trim(SettingsPage.Values[0]), 0);
    if (Port < 1) or (Port > 65535) then
    begin
      MsgBox('Geef een poort tussen 1 en 65535.', mbError, MB_OK);
      Result := False;
      exit;
    end;
    // a silent setup cannot come back to this page; PrepareToInstall
    // checks the ports then
    if WizardSilent then
      exit;
    if not PortsFree(ChosenPorts, Problem) then
    begin
      MsgBox(Problem, mbError, MB_OK);
      Result := False;
    end;
  end;
end;

function AppURL(Param: String): String;
begin
  if HttpsCheck.Checked then
    Result := 'https://localhost:8443'
  else
    Result := 'http://localhost:' + Trim(SettingsPage.Values[0]);
end;

function UpdateReadyMemo(Space, NewLine, MemoUserInfoInfo, MemoDirInfo, MemoTypeInfo,
  MemoComponentsInfo, MemoGroupInfo, MemoTasksInfo: String): String;
begin
  if Upgrade then
    Result := 'Bestaande installatie bijwerken.' + NewLine +
      'De dienst wordt gestopt, bijgewerkt en met de huidige instellingen weer gestart.' + NewLine +
      'Gegevens blijven in ' + ExpandConstant('{commonappdata}\hwlocal') + '; bij een' + NewLine +
      'schemawijziging wordt eerst een backup van de database gemaakt.'
  else
    Result := 'Programma: ' + ExpandConstant('{app}') + NewLine +
      'Gegevens: ' + ExpandConstant('{commonappdata}\hwlocal') + NewLine +
      'Adres: ' + AppURL('') + NewLine + NewLine +
      'hwlocal wordt geïnstalleerd als Windows-dienst die automatisch start.';
end;

function PrepareToInstall(var NeedsRestart: Boolean): String;
var
  RC: Integer;
  Problem: String;
begin
  Result := '';
  if not Upgrade then
  begin
    // checked here too for a silent setup, which skips the check on the
    // settings page; nothing has been installed yet
    if not PortsFree(ChosenPorts, Problem) then
      Result := Problem;
    exit;
  end;
  // stop the running service so its exe can be replaced; the old exe
  // waits until it has stopped
  if FileExists(ExpandConstant('{app}\hwlocal.exe')) then
    Exec(ExpandConstant('{app}\hwlocal.exe'), 'service stop', '', SW_HIDE, ewWaitUntilTerminated, RC)
  else
    Exec(ExpandConstant('{sys}\sc.exe'), 'stop hwlocal', '', SW_HIDE, ewWaitUntilTerminated, RC);
end;

// MoveOldShortcut renames the Start menu shortcut of an install from before
// the rename ("HomeWizard Local"); it keeps the URL with the chosen port.
procedure MoveOldShortcut;
var
  OldDir, Old, NewDir: String;
begin
  OldDir := ExpandConstant('{commonprograms}\HomeWizard Local');
  Old := OldDir + '\HomeWizard Local.url';
  if not FileExists(Old) then
    exit;
  NewDir := ExpandConstant('{group}');
  ForceDirectories(NewDir);
  if RenameFile(Old, NewDir + '\{#AppTitle}.url') then
    RemoveDir(OldDir)
  else
    Log('could not move ' + Old);
end;

procedure CurStepChanged(CurStep: TSetupStep);
var
  RC: Integer;
  Args, LogFile: String;
  Output: AnsiString;
begin
  if CurStep <> ssPostInstall then
    exit;
  if Upgrade then
    MoveOldShortcut;
  WriteToken; // before the service starts, which reads it
  Args := 'service install';
  if not Upgrade then
  begin
    Args := Args + ' -http :' + Trim(SettingsPage.Values[0]);
    if HttpsCheck.Checked then
      Args := Args + ' -tls selfsigned';
  end;
  LogFile := ExpandConstant('{tmp}\service-install.txt');
  WizardForm.StatusLabel.Caption := 'Dienst installeren en starten...';
  if not Exec(ExpandConstant('{cmd}'),
      '/C ""' + ExpandConstant('{app}\hwlocal.exe') + '" ' + Args + ' > "' + LogFile + '" 2>&1"',
      '', SW_HIDE, ewWaitUntilTerminated, RC) or (RC <> 0) then
  begin
    Output := '';
    LoadStringFromFile(LogFile, Output);
    MsgBox('De dienst kon niet worden geïnstalleerd of gestart:' + #13#10 + #13#10 + String(Output) + #13#10 +
      'Zie ook ' + ExpandConstant('{commonappdata}\hwlocal\hwlocal.log') + '.', mbError, MB_OK);
  end
  else
    Log('service install: ' + Args);
end;

procedure CurUninstallStepChanged(CurUninstallStep: TUninstallStep);
var
  RC: Integer;
  Data: String;
begin
  if CurUninstallStep = usUninstall then
    Exec(ExpandConstant('{app}\hwlocal.exe'), 'service uninstall', '', SW_HIDE, ewWaitUntilTerminated, RC);
  if CurUninstallStep = usPostUninstall then
  begin
    Data := ExpandConstant('{commonappdata}\hwlocal');
    if DirExists(Data) and not UninstallSilent and
      (MsgBox('Ook alle gegevens verwijderen (meetgeschiedenis, apparaten, contracten)?' + #13#10 + Data,
        mbConfirmation, MB_YESNO or MB_DEFBUTTON2) = IDYES) then
      DelTree(Data, True, True, True);
  end;
end;
