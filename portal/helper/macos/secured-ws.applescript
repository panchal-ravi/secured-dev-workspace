-- SecuredWS.app URL handler. macOS delivers a registered URL scheme to an app via
-- the GetURL Apple Event (not argv), so the handler must be an AppleScript applet
-- with an `on open location`. We keep the logic in a bundled bash script and just
-- hand it the URL — the applet's only job is to receive the event.
on open location this_URL
	set scriptPath to (POSIX path of (path to me)) & "Contents/Resources/secured-ws.sh"
	try
		do shell script "/bin/bash " & quoted form of scriptPath & " " & quoted form of this_URL
	on error errMsg
		display dialog "Secured Workspace helper failed: " & errMsg buttons {"OK"} default button "OK" with icon stop
	end try
end open location

-- Double-clicking the app (no URL) just explains what it is.
on run
	display dialog "Secured Workspace helper is installed. It runs automatically when you click Authenticate or Open in the developer portal." buttons {"OK"} default button "OK"
end run
