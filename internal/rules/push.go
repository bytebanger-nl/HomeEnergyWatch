package rules

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/netip"
	"net/url"
	"strings"

	"github.com/bytebanger-nl/homeenergywatch/internal/store"
	"github.com/bytebanger-nl/homeenergywatch/internal/webpush"
)

// ErrNoSubscribers means no app has alerts turned on.
var ErrNoSubscribers = errors.New("geen enkele app heeft meldingen aangezet")

// Push sends messages to every stored Web Push subscription.
type Push struct {
	Sender *webpush.Sender
	Store  *store.Store
	Log    *slog.Logger
}

// Notify sends m to all subscriptions and drops the ones that are gone. It
// fails only when no subscription received it.
func (p *Push) Notify(ctx context.Context, m Message) error {
	subs, err := p.Store.PushSubscriptions(ctx)
	if err != nil {
		return err
	}
	return p.send(ctx, subs, m)
}

// NotifyOne sends m to one subscription.
func (p *Push) NotifyOne(ctx context.Context, endpoint string, m Message) error {
	subs, err := p.Store.PushSubscriptions(ctx)
	if err != nil {
		return err
	}
	for _, s := range subs {
		if s.Endpoint == endpoint {
			return p.send(ctx, []store.PushSubscription{s}, m)
		}
	}
	return ErrNoSubscribers
}

func (p *Push) send(ctx context.Context, subs []store.PushSubscription, m Message) error {
	if len(subs) == 0 {
		return ErrNoSubscribers
	}
	payload, err := json.Marshal(m)
	if err != nil {
		return err
	}
	if len(payload) > webpush.MaxPayload {
		m.Body = m.Body[:min(len(m.Body), 1000)]
		if payload, err = json.Marshal(m); err != nil {
			return err
		}
	}
	var errs []string
	ok := 0
	for _, s := range subs {
		err := p.Sender.Send(ctx, webpush.Subscription{Endpoint: s.Endpoint, P256DH: s.P256DH, Auth: s.Auth}, payload, webpush.Options{Topic: topic(m.Tag)})
		switch {
		case err == nil:
			ok++
		case errors.Is(err, webpush.ErrGone):
			p.Log.Info("pushabonnement verlopen, verwijderd", "label", s.Label)
			if derr := p.Store.DeletePushSubscription(ctx, s.Endpoint); derr != nil {
				p.Log.Warn("pushabonnement niet verwijderd", "err", derr)
			}
			errs = append(errs, s.Label+": abonnement verlopen")
		default:
			p.Log.Warn("pushbericht niet verstuurd", "label", s.Label, "err", err)
			errs = append(errs, s.Label+": "+err.Error())
		}
	}
	if ok == 0 {
		return fmt.Errorf("niet verstuurd (%s)", strings.Join(errs, "; "))
	}
	return nil
}

// topic turns a tag into a Topic header value (base64url alphabet, at most
// 32 characters).
func topic(tag string) string {
	var b strings.Builder
	for _, r := range tag {
		if b.Len() == 32 {
			break
		}
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_' {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// CheckEndpoint accepts only https endpoints on public hosts, so the gateway
// cannot be made to post to machines on the LAN.
func CheckEndpoint(endpoint string) error {
	u, err := url.Parse(endpoint)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil {
		return errors.New("het push-adres moet een https-adres zijn")
	}
	host := strings.ToLower(u.Hostname())
	if host == "localhost" || strings.HasSuffix(host, ".localhost") || strings.HasSuffix(host, ".local") || !strings.Contains(host, ".") && net.ParseIP(host) == nil {
		return errors.New("het push-adres moet een pushdienst op internet zijn")
	}
	if ip, err := netip.ParseAddr(host); err == nil {
		if !webpush.PublicAddr(ip) {
			return errors.New("het push-adres moet een pushdienst op internet zijn")
		}
	}
	return nil
}
