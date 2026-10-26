package server

import (
	"reflect"

	"ostiole/internal/discoverylog"
	"ostiole/internal/engine"
	"ostiole/internal/gateway"
	"ostiole/internal/nft"
	"ostiole/internal/policy"
	"ostiole/internal/store"
	"ostiole/internal/sysstat"
	"ostiole/internal/traffic"
	"ostiole/internal/update"
)

var coreAPITypes = []reflect.Type{
	reflect.TypeFor[healthResponse](),
	reflect.TypeFor[servicesStatus](),
	reflect.TypeFor[statusReply](),
	reflect.TypeFor[engine.Drift](),
	reflect.TypeFor[sysstat.Stats](),
	reflect.TypeFor[timezones](),
	reflect.TypeFor[store.Revision](),
	reflect.TypeFor[nft.Counters](),
	reflect.TypeFor[nft.SystemRule](),
	reflect.TypeFor[nft.SystemNAT](),
	reflect.TypeFor[nft.Mapping](),
	reflect.TypeFor[liveLink](),
	reflect.TypeFor[renewRequest](),
	reflect.TypeFor[starterRequest](),
	reflect.TypeFor[configRequest](),
	reflect.TypeFor[applyRequest](),
	reflect.TypeFor[engine.Plan](),
	reflect.TypeFor[engine.ApplyResult](),
	reflect.TypeFor[confirmReply](),
	reflect.TypeFor[dnsCacheClearReply](),
	reflect.TypeFor[dnsName](),
	reflect.TypeFor[lease](),
	reflect.TypeFor[gateway.Status](),
	reflect.TypeFor[detectedGatewayReply](),
	reflect.TypeFor[policyTarget](),
	reflect.TypeFor[policy.Reply](),
	reflect.TypeFor[wireguardKeysRequest](),
	reflect.TypeFor[wireguardKeysReply](),
	reflect.TypeFor[updateCheckReply](),
	reflect.TypeFor[updateStatusReply](),
	reflect.TypeFor[updateApplyRequest](),
	reflect.TypeFor[update.Status](),
	reflect.TypeFor[Overview](),
	reflect.TypeFor[shapingStatus](),
	reflect.TypeFor[gatewayStrip](),
	reflect.TypeFor[gatewayEventsPage](),
	reflect.TypeFor[gateway.Probe](),
	reflect.TypeFor[gateway.Event](),
	reflect.TypeFor[wirelessRadios](),
	reflect.TypeFor[wirelessClient](),
	reflect.TypeFor[trafficLinks](),
	reflect.TypeFor[trafficDevices](),
	reflect.TypeFor[trafficDevice](),
	reflect.TypeFor[trafficDestinations](),
	reflect.TypeFor[trafficClearReply](),
	reflect.TypeFor[traffic.Event](),
	reflect.TypeFor[discoveryLogPage](),
	reflect.TypeFor[discoverylog.Event](),
	reflect.TypeFor[discoveryAnnouncementsReply](),
	reflect.TypeFor[wakeRequest](),
}
