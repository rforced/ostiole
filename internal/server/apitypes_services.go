package server

import (
	"reflect"

	"ostiole/internal/acme"
	"ostiole/internal/ddns"
	"ostiole/internal/diag"
	"ostiole/internal/dnsblock"
	"ostiole/internal/dnsprovider"
	"ostiole/internal/feeds"
	"ostiole/internal/modem"
	"ostiole/internal/requestlog"
	"ostiole/internal/wg"
)

var servicesAPITypes = []reflect.Type{
	reflect.TypeFor[blockingState](),
	reflect.TypeFor[blocklistRefreshReply](),
	reflect.TypeFor[blocklistImportReply](),
	reflect.TypeFor[dnsblock.CatalogEntry](),
	reflect.TypeFor[dnsblock.Finding](),
	reflect.TypeFor[dnsblock.Page](),
	reflect.TypeFor[feeds.Status](),
	reflect.TypeFor[feeds.Page](),
	reflect.TypeFor[feeds.Part](),
	reflect.TypeFor[feedRefreshReply](),
	reflect.TypeFor[inspectFeedRequest](),
	reflect.TypeFor[pingRequest](),
	reflect.TypeFor[diag.PingResult](),
	reflect.TypeFor[traceRequest](),
	reflect.TypeFor[diag.TraceResult](),
	reflect.TypeFor[captureRequest](),
	reflect.TypeFor[diag.JournalEntry](),
	reflect.TypeFor[diag.StatesResult](),
	reflect.TypeFor[diag.Neighbour](),
	reflect.TypeFor[modem.Status](),
	reflect.TypeFor[certificatesResponse](),
	reflect.TypeFor[certificateResponse](),
	reflect.TypeFor[certificateBundleReply](),
	reflect.TypeFor[certificateKeyReply](),
	reflect.TypeFor[selfSignedRequest](),
	reflect.TypeFor[pkcs12Request](),
	reflect.TypeFor[acme.Status](),
	reflect.TypeFor[idReply](),
	reflect.TypeFor[proxyStatus](),
	reflect.TypeFor[wafEventsPage](),
	reflect.TypeFor[requestsPage](),
	reflect.TypeFor[requestlog.Request](),
	reflect.TypeFor[ddnsResponse](),
	reflect.TypeFor[ddnsCheckRequest](),
	reflect.TypeFor[ddns.CheckResult](),
	reflect.TypeFor[providerTestRequest](),
	reflect.TypeFor[dnsprovider.TestResult](),
	reflect.TypeFor[tailscaleStatus](),
	reflect.TypeFor[tailscaleLoginRequest](),
	reflect.TypeFor[tailscaleLoginReply](),
	reflect.TypeFor[ntpStatus](),
	reflect.TypeFor[openvpnStatus](),
	reflect.TypeFor[wg.Tunnel](),
}
