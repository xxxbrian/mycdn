package main

import (
	"mycdn/internal/analytics"
	"mycdn/internal/gateway"
)

// The observer is deliberately a value adapter. The analytical store owns
// admission, batching and persistence; the gateway never waits for any of them.
type observer struct{ store *analytics.Store }

func analyticsObserver(store *analytics.Store) gateway.Observer {
	if store == nil {
		return nil
	}
	return observer{store: store}
}

func (o observer) RequestCompleted(event gateway.RequestEvent) {
	o.store.RecordRequest(analytics.RequestEvent(event))
}

func (o observer) OriginCompleted(event gateway.OriginEvent) {
	o.store.RecordOrigin(analytics.OriginEvent(event))
}

func (o observer) Transfer(event gateway.TransferEvent) {
	o.store.RecordTransfer(analytics.TransferEvent(event))
}
