package browser

import (
	"reflect"
	"unsafe"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/cdp"
)

func detach(rb *rod.Browser) bool {
	if rb == nil {
		return true
	}

	f := reflect.ValueOf(rb).Elem().FieldByName("client")
	if !nilable(f) || f.IsNil() {
		return false
	}
	client, ok := reflect.TypeAssert[*cdp.Client](reflect.NewAt(f.Type(), unsafe.Pointer(f.UnsafeAddr())).Elem())
	if !ok || client == nil {
		return false
	}

	wf := reflect.ValueOf(client).Elem().FieldByName("ws")
	if !nilable(wf) || wf.IsNil() {
		return false
	}
	ws, ok := reflect.TypeAssert[*cdp.WebSocket](reflect.NewAt(wf.Type(), unsafe.Pointer(wf.UnsafeAddr())).Elem())
	if !ok || ws == nil {
		return false
	}
	_ = ws.Close()
	return true
}

func nilable(v reflect.Value) bool {
	if !v.IsValid() {
		return false
	}
	switch v.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return true
	}
	return false
}
