//go:build !server

package main

import (
	"context"
	"omni_money/backend/core"
)

func (a *App) GetSubscriptions() ([]core.SubscriptionRule, error) {
	service, release, err := a.borrowService()
	if err != nil {
		return nil, err
	}
	defer release()
	return service.Subscriptions()
}

func (a *App) SaveSubscription(id string, input core.SubscriptionInput) (*core.SubscriptionRule, error) {
	service, release, err := a.borrowService()
	if err != nil {
		return nil, err
	}
	defer release()
	return service.SaveSubscription(id, input)
}

func (a *App) SyncSubscriptions() (*core.SubscriptionSync, error) {
	service, release, err := a.borrowService()
	if err != nil {
		return nil, err
	}
	defer release()
	return service.SyncSubscriptions(context.Background())
}

func (a *App) GetSubscriptionTransactions(id, account, search, start, end string, offset int) (*core.SubscriptionHistory, error) {
	service, release, err := a.borrowService()
	if err != nil {
		return nil, err
	}
	defer release()
	return service.SubscriptionTransactions(id, account, search, start, end, offset)
}

func (a *App) GetSubscriptionCalendar(month string) ([]core.SubscriptionCalendarEntry, error) {
	service, release, err := a.borrowService()
	if err != nil {
		return nil, err
	}
	defer release()
	return service.SubscriptionCalendar(month)
}

func (a *App) GetSubscriptionCandidates() ([]core.SubscriptionCandidate, error) {
	service, release, err := a.borrowService()
	if err != nil {
		return nil, err
	}
	defer release()
	return service.SubscriptionCandidates()
}

func (a *App) DismissSubscriptionCandidate(fingerprint string) error {
	service, release, err := a.borrowService()
	if err != nil {
		return err
	}
	defer release()
	return service.DismissSubscriptionCandidate(fingerprint)
}
