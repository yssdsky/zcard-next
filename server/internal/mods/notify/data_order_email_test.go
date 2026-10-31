package notify

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"testing"

	storefrontv1 "github.com/NovaWorks/zcard-next/server/api/storefront/v1"
	"github.com/NovaWorks/zcard-next/server/internal/data/ent/notificationlog"
	"github.com/NovaWorks/zcard-next/server/internal/mods/inventory"
	notifyport "github.com/NovaWorks/zcard-next/server/internal/mods/notify/port"
	ordermod "github.com/NovaWorks/zcard-next/server/internal/mods/order"
	"github.com/NovaWorks/zcard-next/server/internal/mods/settings"
	"github.com/NovaWorks/zcard-next/server/internal/platform/events"
	"github.com/NovaWorks/zcard-next/server/internal/platform/i18n"
)

func TestOrderPaidAndDeliveredDefaultEmailThroughSMTP(t *testing.T) {
	for _, event := range []string{"order.paid", "order.delivered"} {
		for _, locale := range []i18n.Locale{i18n.ZhCN, i18n.En} {
			t.Run(event+"/"+string(locale), func(t *testing.T) {
				r := newNotifyRepo(t)
				ctx := i18n.WithLocale(context.Background(), locale)
				const orderID, userID = uint64(9007199254740997), uint64(9007199254740999)
				r.data.Client.Order.Create().SetID(orderID).SetOrderNo("MAIL-ORDER").SetUserID(userID).SetContact("Checkout@example.test").SetQueryPasswordHash("never-send-password-hash").SetTotalAmount(1234).SaveX(ctx)
				if err := settings.NewRepoImpl(r.data).Put(ctx, "site", "url", []byte(`"https://shop.example.test"`)); err != nil {
					t.Fatal(err)
				}
				if _, err := r.UpsertTemplate(ctx, event, "inbox", "zh_CN", "Order", "{{.order_no}}", true); err != nil {
					t.Fatal(err)
				}
				port, done := smtpFixture(t, false)
				email := NewEmailChannel(fakeSettings{values: map[string]string{"notify.smtp_host": `"127.0.0.1"`, "notify.smtp_port": fmt.Sprint(port), "notify.smtp_user": `"sender@example.test"`, "notify.smtp_password": `"fixture-secret"`}})
				payload, _ := json.Marshal(map[string]any{"order_id": orderID, "order_no": "MAIL-ORDER", "user_id": 7, "email": "wrong@example.test"})
				if err := NewDispatcher(r, email, NewInboxChannel(r)).HandleEvent(ctx, events.Envelope{Type: event, Payload: payload}); err != nil {
					t.Fatal(err)
				}
				capture := <-done
				if capture.err != "" || !strings.Contains(capture.body, "To: checkout@example.test") || !strings.Contains(capture.body, "https://shop.example.test/fetch?order_no=MAIL-ORDER") || strings.Contains(capture.body, "never-send-password-hash") {
					t.Fatal("order notification did not reach the checkout email with a safe link", capture.err, capture.body)
				}
				if locale == i18n.En && (!strings.Contains(capture.body, "View your order and delivery results") || strings.Contains(capture.body, "查询密码")) {
					t.Fatal("English notification used Chinese content", capture.body)
				}
				row := r.data.Client.NotificationLog.Query().Where(notificationlog.ChannelEQ(notificationlog.ChannelEmail)).OnlyX(ctx)
				if row.Status != notificationlog.StatusSent || row.BizID != orderID || strings.Contains(row.Body, "never-send-password-hash") {
					t.Fatal("notification logging lost the exact order identity or exposed a secret", row)
				}
				messages, _, err := r.ListInbox(ctx, userID, false, 1, 10)
				if err != nil || len(messages) != 1 {
					t.Fatal("large user ID was truncated during event decoding", err)
				}
			})
		}
	}
}

func TestVirtualGuestCodeThroughConfiguredSMTP(t *testing.T) {
	r := newNotifyRepo(t)
	ctx := context.Background()
	o := r.data.Client.Order.Create().SetOrderNo("VIRTUAL-MAIL-CODE").SetContact("guest@example.test").SetQueryPasswordHash("original-password-hash").SaveX(ctx)
	cipher, err := inventory.NewCardCipher(make([]byte, 32))
	if err != nil {
		t.Fatal(err)
	}
	port, done := smtpFixture(t, false)
	email := NewEmailChannel(fakeSettings{values: map[string]string{"notify.smtp_host": `"127.0.0.1"`, "notify.smtp_port": fmt.Sprint(port), "notify.smtp_user": `"sender@example.test"`, "notify.smtp_password": `"fixture-secret"`}})
	svc := ordermod.NewStoreOrderService(&ordermod.OrderUsecase{Data: r.data, AccessCipher: cipher}, nil)
	svc.SetRecoverySender(NewDispatcher(r, email))
	if _, err := svc.SendOrderAccessCode(ctx, &storefrontv1.OrderAccessCodeRequest{OrderNo: o.OrderNo, Email: o.Contact}); err != nil {
		t.Fatal(err)
	}
	capture := <-done
	code := regexp.MustCompile(`<b>([0-9]{6})</b>`).FindStringSubmatch(capture.body)
	if capture.err != "" || len(code) != 2 {
		t.Fatal("ordinary virtual guest could not receive an SMTP code", capture.err)
	}
	credential, err := svc.RecoverOrderAccess(ctx, &storefrontv1.OrderAccessRecoveryRequest{OrderNo: o.OrderNo, Email: o.Contact, Code: code[1]})
	if err != nil || credential.GetOrderAccessToken() == "" {
		t.Fatal("SMTP code could not recover the virtual order", err)
	}
	log := r.data.Client.NotificationLog.Query().OnlyX(ctx)
	if log.Status != notificationlog.StatusSent || strings.Contains(log.Body, code[1]) || strings.Contains(fmt.Sprint(log.Variables), code[1]) {
		t.Fatal("SMTP code was not delivered or leaked to notification logs", log)
	}
}

func TestOrderEmailTemplateFallbackHonorsCustomAndDisabled(t *testing.T) {
	r := newNotifyRepo(t)
	ctx := context.Background()
	defaultTpl, err := r.Template(ctx, "order.paid", "email", "en")
	if err != nil || !strings.Contains(defaultTpl.BodyTpl, "View your order") {
		t.Fatal("missing default order email", err)
	}
	for _, enabled := range []bool{true, false} {
		custom, err := r.UpsertTemplate(ctx, "order.paid", "email", "zh_CN", "custom", "custom body", enabled)
		if err != nil {
			t.Fatal(err)
		}
		got, err := r.Template(ctx, "order.paid", "email", "en")
		if enabled && (err != nil || got.ID != custom.ID) {
			t.Fatal("merchant template was overridden", got, err)
		}
		if !enabled && err != ErrNotFound {
			t.Fatal("disabled merchant email fell back to an enabled default", got, err)
		}
	}
}

func TestOrderNotificationRejectsCrossTenantAndMismatchedIdentity(t *testing.T) {
	r := newNotifyRepo(t)
	ctx := context.Background()
	o := r.data.Client.Order.Create().SetOrderNo("PRIVATE-ORDER").SetSubsiteID(5).SetUserID(7).SetContact("private@example.test").SaveX(ctx)
	_, err := r.UpsertTemplate(ctx, "order.paid", "inbox", "zh_CN", "custom", "private", true)
	if err != nil {
		t.Fatal(err)
	}
	dispatcher := NewDispatcher(r, NewEmailChannel(fakeSettings{}), NewInboxChannel(r))
	for _, event := range []events.Envelope{
		{Type: "order.paid", SubsiteID: 9, Payload: json.RawMessage(fmt.Sprintf(`{"order_id":%d,"order_no":%q,"email":"attacker@example.test","user_id":99,"fetch_url":"https://evil.example"}`, o.ID, o.OrderNo))},
		{Type: "order.paid", SubsiteID: 5, Payload: json.RawMessage(fmt.Sprintf(`{"order_id":%d,"order_no":%q,"email":"attacker@example.test","user_id":99}`, o.ID+1, o.OrderNo))},
	} {
		if err := dispatcher.HandleEvent(ctx, event); err != nil {
			t.Fatal(err)
		}
	}
	if r.data.Client.Notification.Query().CountX(ctx) != 0 || r.data.Client.NotificationLog.Query().Where(notificationlog.StatusEQ(notificationlog.StatusSent)).CountX(ctx) != 0 {
		t.Fatal("unresolved order identity delivered an email or inbox message")
	}
	for _, log := range r.data.Client.NotificationLog.Query().AllX(ctx) {
		if log.Variables["user_id"] != "" || log.Variables["fetch_url"] != "" || log.BizID != 0 {
			t.Fatal("unresolved identity survived notification hydration")
		}
	}
}

func TestMandatoryRecoveryMailDoesNotReportSkippedAsSent(t *testing.T) {
	r := newNotifyRepo(t)
	ctx := context.Background()
	dispatcher := NewDispatcher(r, NewEmailChannel(fakeSettings{}))
	err := dispatcher.Send(ctx, notifyport.Message{Channel: "email", Recipient: "guest@example.test", BizType: "order_access_recovery", EventType: "order.access_code", Body: "123456"})
	if err != ErrSkipped {
		t.Fatal("unconfigured mandatory recovery mail reported success", err)
	}
	row := r.data.Client.NotificationLog.Query().OnlyX(ctx)
	if row.Status != notificationlog.StatusSkipped || strings.Contains(row.Body, "123456") {
		t.Fatal("skipped verification mail was logged as sent or exposed its code", row)
	}
}

func TestOrderEmailMissingStoreURLIsDiagnosable(t *testing.T) {
	r := newNotifyRepo(t)
	ctx := context.Background()
	o := r.data.Client.Order.Create().SetOrderNo("NO-STORE-URL").SetContact("guest@example.test").SaveX(ctx)
	payload, _ := json.Marshal(map[string]any{"order_id": o.ID, "order_no": o.OrderNo})
	if err := NewDispatcher(r, NewEmailChannel(fakeSettings{})).HandleEvent(ctx, events.Envelope{Type: "order.paid", Payload: payload}); err != nil {
		t.Fatal(err)
	}
	row := r.data.Client.NotificationLog.Query().OnlyX(ctx)
	if row.Status != notificationlog.StatusFailed || !strings.Contains(row.ErrorMessage, "店铺网址") {
		t.Fatal("missing store URL silently discarded the notification", row)
	}
}

func TestOrderEmailTemplateReadErrorDoesNotUseDefault(t *testing.T) {
	r := newNotifyRepo(t)
	if err := r.data.DB.Close(); err != nil {
		t.Fatal(err)
	}
	if tpl, err := r.Template(context.Background(), "order.paid", "email", "zh_CN"); err == nil || err == ErrNotFound || tpl != nil {
		t.Fatal("template read failure was mistaken for a missing merchant template", tpl, err)
	}
}
