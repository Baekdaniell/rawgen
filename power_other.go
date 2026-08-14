//go:build !windows

package main

func startKeepAwake() func() { return func() {} }
