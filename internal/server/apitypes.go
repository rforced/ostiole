package server

import (
	"reflect"
	"slices"
)

// APITypes lists the named types the API writes as JSON replies and reads
// as request bodies, so internal/tsgen can type the web client from them.
func APITypes() []reflect.Type {
	return slices.Concat(coreAPITypes, accountAPITypes, servicesAPITypes)
}
