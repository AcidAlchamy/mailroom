# Windows toast notification for Mailroom desk items.
#
#   notify_cmd: powershell -NoProfile -ExecutionPolicy Bypass -File <path>\notify-windows.ps1 -Title "{title}" -Body "{body}" -Id "{id}" -Project "{project}"
#
# Uses the built-in WinRT toast API — no modules to install. Toasts require a registered
# AppUserModelID, so we borrow PowerShell's own; an unregistered id fails silently.
param(
  [string]$Title   = "Mailroom",
  [string]$Body    = "",
  [string]$Id      = "",
  [string]$Project = ""
)

$ErrorActionPreference = "Stop"
$AppId = '{1AC14E77-02E7-4E5D-B744-2EB1AE5198B7}\WindowsPowerShell\v1.0\powershell.exe'

try {
  [void][Windows.UI.Notifications.ToastNotificationManager, Windows.UI.Notifications, ContentType=WindowsRuntime]
  [void][Windows.Data.Xml.Dom.XmlDocument, Windows.Data.Xml.Dom, ContentType=WindowsRuntime]

  # Line 3 is the command to answer, so the toast is self-contained: no hunting for a terminal.
  $answer = if ($Id) { "mailroom desk answer $Id <option>" } else { "mailroom desk" }

  $xml = @"
<toast scenario="reminder">
  <visual>
    <binding template="ToastGeneric">
      <text>$([System.Security.SecurityElement]::Escape($Title))</text>
      <text>$([System.Security.SecurityElement]::Escape($Body))</text>
      <text placement="attribution">$([System.Security.SecurityElement]::Escape($answer))</text>
    </binding>
  </visual>
  <audio src="ms-winsoundevent:Notification.Reminder"/>
</toast>
"@

  $doc = [Windows.Data.Xml.Dom.XmlDocument]::new()
  $doc.LoadXml($xml)
  $toast = [Windows.UI.Notifications.ToastNotification]::new($doc)
  [Windows.UI.Notifications.ToastNotificationManager]::CreateToastNotifier($AppId).Show($toast)
}
catch {
  # Never let a failed notification break the mailbox. Fall back to a message box.
  try {
    Add-Type -AssemblyName PresentationFramework
    [void][System.Windows.MessageBox]::Show("$Body`n`n$Id", $Title)
  } catch { }
}
