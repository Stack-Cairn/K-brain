//go:build windows

package sandbox

import (
	"errors"
	"fmt"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

// The offline account's network block: persistent Windows Filtering Platform
// filters, in a sublayer of their own with the highest weight, that block
// every connect, accept, listen and bind of a process whose token is a member
// of the offline group (ALE_USER_ID). Membership is tested with an access
// check against a security descriptor that grants only that group, so it holds
// for the restricted token of the command as well (the group is one of its
// restricting SIDs) and for anything it starts.
//
// Loopback is blocked too: the command talks to K-brain over its standard
// handles, and a loopback connection would reach the user's services, local
// proxies and debuggers, which are as much a way out as the internet.
//
// The filters do not cover services that act on a process's behalf (the DNS
// client resolving a name, BITS); a name lookup can still leave the machine.

var (
	modfwpuclnt                = windows.NewLazySystemDLL("fwpuclnt.dll")
	procFwpmEngineOpen0        = modfwpuclnt.NewProc("FwpmEngineOpen0")
	procFwpmEngineClose0       = modfwpuclnt.NewProc("FwpmEngineClose0")
	procFwpmTransactionBegin0  = modfwpuclnt.NewProc("FwpmTransactionBegin0")
	procFwpmTransactionCommit0 = modfwpuclnt.NewProc("FwpmTransactionCommit0")
	procFwpmTransactionAbort0  = modfwpuclnt.NewProc("FwpmTransactionAbort0")
	procFwpmProviderAdd0       = modfwpuclnt.NewProc("FwpmProviderAdd0")
	procFwpmProviderDelete0    = modfwpuclnt.NewProc("FwpmProviderDeleteByKey0")
	procFwpmSubLayerAdd0       = modfwpuclnt.NewProc("FwpmSubLayerAdd0")
	procFwpmSubLayerDelete0    = modfwpuclnt.NewProc("FwpmSubLayerDeleteByKey0")
	procFwpmFilterAdd0         = modfwpuclnt.NewProc("FwpmFilterAdd0")
	procFwpmFilterDelete0      = modfwpuclnt.NewProc("FwpmFilterDeleteByKey0")
	procFwpmFilterGetByKey0    = modfwpuclnt.NewProc("FwpmFilterGetByKey0")
	procFwpmFreeMemory0        = modfwpuclnt.NewProc("FwpmFreeMemory0")
)

const (
	rpcCAuthnWinNT = 10

	fwpEmpty                  = 0
	fwpSecurityDescriptorType = 14
	fwpMatchEqual             = 0
	fwpActionBlock            = 0x1001

	fwpmProviderFlagPersistent    = 1
	fwpmSublayerFlagPersistent    = 1
	fwpmFilterFlagPersistent      = 1
	fwpmFilterFlagClearActionRigh = 8

	fwpEFilterNotFound   = 0x80320003
	fwpEProviderNotFound = 0x80320005
	fwpESublayerNotFound = 0x80320007
	fwpENotFound         = 0x80320008
	fwpEAlreadyExists    = 0x80320009
)

type fwpmDisplayData0 struct {
	name        *uint16
	description *uint16
}

type fwpByteBlob struct {
	size uint32
	data *byte
}

type fwpmProvider0 struct {
	providerKey  windows.GUID
	displayData  fwpmDisplayData0
	flags        uint32
	providerData fwpByteBlob
	serviceName  *uint16
}

type fwpmSublayer0 struct {
	subLayerKey  windows.GUID
	displayData  fwpmDisplayData0
	flags        uint16
	providerKey  *windows.GUID
	providerData fwpByteBlob
	weight       uint16
}

type fwpValue0 struct {
	typ   uint32
	value uintptr
}

type fwpmFilterCondition0 struct {
	fieldKey  windows.GUID
	matchType uint32
	value     fwpValue0
}

type fwpmAction0 struct {
	typ        uint32
	filterType windows.GUID
}

type fwpmFilter0 struct {
	filterKey           windows.GUID
	displayData         fwpmDisplayData0
	flags               uint32
	providerKey         *windows.GUID
	providerData        fwpByteBlob
	layerKey            windows.GUID
	subLayerKey         windows.GUID
	weight              fwpValue0
	numFilterConditions uint32
	filterCondition     *fwpmFilterCondition0
	action              fwpmAction0
	providerContext     [2]uint64
	reserved            *windows.GUID
	filterID            uint64
	effectiveWeight     fwpValue0
}

func mustGUID(s string) windows.GUID {
	g, err := windows.GUIDFromString(s)
	if err != nil {
		panic(err)
	}
	return g
}

var (
	fwpmConditionALEUserID = mustGUID("{af043a0a-b34d-4f86-979c-c90371af6e66}")

	// wfpLayers are the layers the offline account is blocked on.
	wfpLayers = []struct {
		label string
		key   windows.GUID
	}{
		{"connect-v4", mustGUID("{c38d57d1-05a7-4c33-904f-7fbceee60e82}")},
		{"connect-v6", mustGUID("{4a72393b-319f-44bc-84c3-ba54dcb3b6b4}")},
		{"recv-accept-v4", mustGUID("{e1cd9fe7-f4b5-4273-96c0-592e487b8650}")},
		{"recv-accept-v6", mustGUID("{a3b42c97-9f04-4672-b87e-cee9c483257f}")},
		{"listen-v4", mustGUID("{88bb5dad-76d7-4227-9c71-df0a3ed7be7e}")},
		{"listen-v6", mustGUID("{7ac9de24-17dd-4814-b4bd-a9fbc95a321b}")},
		{"resource-assignment-v4", mustGUID("{1247d66d-0b60-4a15-8d44-7155d0f53a0c}")},
		{"resource-assignment-v6", mustGUID("{55a650e1-5f0a-4eca-a653-88f53b26aa8c}")},
	}
)

func (n setupNames) wfpGUID(label string) windows.GUID {
	g := n.guid(label)
	return *(*windows.GUID)(unsafe.Pointer(&g))
}

func wfpCall(p *windows.LazyProc, args ...uintptr) error {
	r, _, _ := p.Call(args...)
	if r != 0 {
		return syscall.Errno(r)
	}
	return nil
}

func isWFPNotFound(err error) bool {
	var errno syscall.Errno
	if !errors.As(err, &errno) {
		return false
	}
	switch uint32(errno) {
	case fwpEFilterNotFound, fwpEProviderNotFound, fwpESublayerNotFound, fwpENotFound:
		return true
	}
	return false
}

func wfpOpen() (windows.Handle, error) {
	var engine windows.Handle
	if err := wfpCall(procFwpmEngineOpen0, 0, rpcCAuthnWinNT, 0, 0, uintptr(unsafe.Pointer(&engine))); err != nil {
		return 0, fmt.Errorf("open the filtering engine: %w", err)
	}
	return engine, nil
}

// wfpTransaction runs fn in a transaction of engine.
func wfpTransaction(engine windows.Handle, fn func() error) error {
	if err := wfpCall(procFwpmTransactionBegin0, uintptr(engine), 0); err != nil {
		return fmt.Errorf("begin a filtering transaction: %w", err)
	}
	if err := fn(); err != nil {
		_ = wfpCall(procFwpmTransactionAbort0, uintptr(engine))
		return err
	}
	if err := wfpCall(procFwpmTransactionCommit0, uintptr(engine)); err != nil {
		return fmt.Errorf("commit the filtering transaction: %w", err)
	}
	return nil
}

// installWFP replaces the offline account's filters (removing any left by an
// earlier setup first) so that they block offlineGroupSID.
func installWFP(n setupNames, offlineGroupSID string) error {
	engine, err := wfpOpen()
	if err != nil {
		return err
	}
	defer wfpCall(procFwpmEngineClose0, uintptr(engine))
	sd, err := windows.SecurityDescriptorFromString(fmt.Sprintf("O:SYG:SYD:(A;;CC;;;%s)", offlineGroupSID))
	if err != nil {
		return err
	}
	sdBytes := unsafe.Slice((*byte)(unsafe.Pointer(sd)), sd.Length())
	return wfpTransaction(engine, func() error {
		if err := deleteWFPObjects(engine, n); err != nil {
			return err
		}
		providerKey := n.wfpGUID("provider")
		sublayerKey := n.wfpGUID("sublayer")
		name, _ := windows.UTF16PtrFromString("K-brain sandbox " + n.key)
		desc, _ := windows.UTF16PtrFromString("Blocks the network for the K-brain offline sandbox account " + n.offline)
		provider := fwpmProvider0{providerKey: providerKey, displayData: fwpmDisplayData0{name, desc}, flags: fwpmProviderFlagPersistent}
		if err := wfpCall(procFwpmProviderAdd0, uintptr(engine), uintptr(unsafe.Pointer(&provider)), 0); err != nil {
			return fmt.Errorf("add the filtering provider: %w", err)
		}
		sublayer := fwpmSublayer0{subLayerKey: sublayerKey, displayData: fwpmDisplayData0{name, desc}, flags: fwpmSublayerFlagPersistent, providerKey: &providerKey, weight: 0xFFFF}
		if err := wfpCall(procFwpmSubLayerAdd0, uintptr(engine), uintptr(unsafe.Pointer(&sublayer)), 0); err != nil {
			return fmt.Errorf("add the filtering sublayer: %w", err)
		}
		blob := fwpByteBlob{size: uint32(len(sdBytes)), data: &sdBytes[0]}
		cond := fwpmFilterCondition0{fieldKey: fwpmConditionALEUserID, matchType: fwpMatchEqual, value: fwpValue0{typ: fwpSecurityDescriptorType, value: uintptr(unsafe.Pointer(&blob))}}
		for _, layer := range wfpLayers {
			filter := fwpmFilter0{
				filterKey:           n.wfpGUID("filter-" + layer.label),
				displayData:         fwpmDisplayData0{name, desc},
				flags:               fwpmFilterFlagPersistent | fwpmFilterFlagClearActionRigh,
				providerKey:         &providerKey,
				layerKey:            layer.key,
				subLayerKey:         sublayerKey,
				weight:              fwpValue0{typ: fwpEmpty},
				numFilterConditions: 1,
				filterCondition:     &cond,
				action:              fwpmAction0{typ: fwpActionBlock},
			}
			var id uint64
			if err := wfpCall(procFwpmFilterAdd0, uintptr(engine), uintptr(unsafe.Pointer(&filter)), 0, uintptr(unsafe.Pointer(&id))); err != nil {
				return fmt.Errorf("add the %s filter: %w", layer.label, err)
			}
		}
		return nil
	})
}

// removeWFP removes the offline account's filters, sublayer and provider.
// Objects that do not exist are skipped.
func removeWFP(n setupNames) error {
	engine, err := wfpOpen()
	if err != nil {
		return err
	}
	defer wfpCall(procFwpmEngineClose0, uintptr(engine))
	return wfpTransaction(engine, func() error { return deleteWFPObjects(engine, n) })
}

func deleteWFPObjects(engine windows.Handle, n setupNames) error {
	del := func(p *windows.LazyProc, key windows.GUID, what string) error {
		if err := wfpCall(p, uintptr(engine), uintptr(unsafe.Pointer(&key))); err != nil && !isWFPNotFound(err) {
			return fmt.Errorf("remove the %s: %w", what, err)
		}
		return nil
	}
	for _, layer := range wfpLayers {
		if err := del(procFwpmFilterDelete0, n.wfpGUID("filter-"+layer.label), layer.label+" filter"); err != nil {
			return err
		}
	}
	if err := del(procFwpmSubLayerDelete0, n.wfpGUID("sublayer"), "filtering sublayer"); err != nil {
		return err
	}
	return del(procFwpmProviderDelete0, n.wfpGUID("provider"), "filtering provider")
}

// wfpFiltersPresent counts the offline account's filters that exist. Reading
// filters needs administrator rights; the error says so.
func wfpFiltersPresent(n setupNames) (int, error) {
	engine, err := wfpOpen()
	if err != nil {
		return 0, err
	}
	defer wfpCall(procFwpmEngineClose0, uintptr(engine))
	count := 0
	for _, layer := range wfpLayers {
		key := n.wfpGUID("filter-" + layer.label)
		var filter uintptr
		err := wfpCall(procFwpmFilterGetByKey0, uintptr(engine), uintptr(unsafe.Pointer(&key)), uintptr(unsafe.Pointer(&filter)))
		if isWFPNotFound(err) {
			continue
		}
		if err != nil {
			return count, err
		}
		count++
		_, _, _ = procFwpmFreeMemory0.Call(uintptr(unsafe.Pointer(&filter)))
	}
	return count, nil
}
