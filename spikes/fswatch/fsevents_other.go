//go:build !darwin

package main

import "errors"

func newFSEvents(string, func(string), wopts, *wstats) (watcher, error) {
	return nil, errors.New("fsevents: darwin only")
}
