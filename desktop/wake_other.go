//go:build !darwin

package main

func observeSystemWake(func()) func() { return func() {} }
