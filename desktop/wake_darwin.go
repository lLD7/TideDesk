package main

import "github.com/ebitengine/purego/objc"

// NSWorkspace notifications are delivered independently of window activation.
func observeSystemWake(wake func()) func() {
	sel := objc.RegisterName
	workspace := objc.ID(objc.GetClass("NSWorkspace")).Send(sel("sharedWorkspace"))
	center := workspace.Send(sel("notificationCenter"))
	name := objc.ID(objc.GetClass("NSString")).Send(sel("stringWithUTF8String:"), "NSWorkspaceDidWakeNotification\x00")
	block := objc.NewBlock(func(_ objc.Block, _ objc.ID) { wake() })
	observer := center.Send(sel("addObserverForName:object:queue:usingBlock:"), name, objc.ID(0), objc.ID(0), block)
	return func() {
		center.Send(sel("removeObserver:"), observer)
		block.Release()
	}
}
