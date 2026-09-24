package main

import (
	"sync"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

type firstQuota struct {
	observation quotaObservation
	reason      string
}

func fetchInitialUsages(auths []pluginapi.HostAuthFileEntry, fetch func(pluginapi.HostAuthFileEntry) (quotaObservation, string)) []firstQuota {
	results := make([]firstQuota, len(auths))
	sem := make(chan struct{}, 3)
	var fetches sync.WaitGroup
	for index, auth := range auths {
		fetches.Add(1)
		go func(index int, auth pluginapi.HostAuthFileEntry) {
			defer fetches.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			results[index].observation, results[index].reason = fetch(auth)
		}(index, auth)
	}
	fetches.Wait()
	return results
}
