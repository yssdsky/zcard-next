package notify

import (
	"context"
	"fmt"
	"net/url"
	"strconv"
	"strings"

	"github.com/NovaWorks/zcard-next/server/internal/data"
	"github.com/NovaWorks/zcard-next/server/internal/data/ent"
	"github.com/NovaWorks/zcard-next/server/internal/data/ent/order"
	"github.com/NovaWorks/zcard-next/server/internal/data/ent/orderdelivery"
	"github.com/NovaWorks/zcard-next/server/internal/data/ent/resellersite"
	"github.com/NovaWorks/zcard-next/server/internal/mods/settings"
	"github.com/NovaWorks/zcard-next/server/internal/platform/events"
	"github.com/NovaWorks/zcard-next/server/internal/platform/money"
	"github.com/NovaWorks/zcard-next/server/internal/platform/orderaccess"
)

// Resolve delivery targets from the tenant's order, never from an event's email field.
// Notification links carry no password, card content, or order access credential.
func (r *NotifyRepo) orderEmailVars(ctx context.Context, env events.Envelope, vars map[string]string) error {
	rawID := vars["order_id"]
	vars["email"] = ""
	vars["user_id"], vars["order_id"], vars["fetch_url"], vars["cards_count"] = "", "", "", ""
	no := vars["order_no"]
	if no == "" {
		no = env.AggregateID
	}
	q := data.Client(ctx, r.data).Order.Query().Where(order.OrderNo(no), order.SubsiteID(env.SubsiteID))
	if rawID != "" {
		id, err := strconv.ParseUint(rawID, 10, 64)
		if err != nil {
			return fmt.Errorf("notify: 订单标识无效: %w", err)
		}
		q.Where(order.ID(id))
	}
	o, err := q.Only(ctx)
	if ent.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return err
	}
	vars["order_no"], vars["order_id"], vars["user_id"] = o.OrderNo, strconv.FormatUint(o.ID, 10), strconv.FormatUint(o.UserID, 10)
	vars["email"] = orderaccess.ContactEmail(o.Contact, o.GuestContact)
	if vars["email"] == "" && o.UserID > 0 {
		u, e := data.Client(ctx, r.data).User.Get(ctx, o.UserID)
		if e != nil && !ent.IsNotFound(e) {
			return e
		}
		if u != nil {
			vars["email"] = orderaccess.ContactEmail(u.Email, "")
		}
	}
	currency := o.BaseCurrency
	if currency == "" {
		currency = data.DefaultBaseCurrency
	}
	vars["amount"] = currency + " " + money.Cents(o.TotalAmount).Format(money.DefaultDisplayPrecision)
	count, err := data.Client(ctx, r.data).OrderDelivery.Query().Where(orderdelivery.OrderID(o.ID), orderdelivery.CardIDGT(0)).Count(ctx)
	if err != nil {
		return err
	}
	vars["cards_count"] = strconv.Itoa(count)
	base := ""
	if o.SubsiteID == 0 {
		raw, e := settings.NewRepoImpl(r.data).GetDefault(ctx, "site", "url", nil)
		if e == nil && len(raw) > 0 {
			if err := jsonUnmarshal(raw, &base); err != nil {
				return err
			}
		} else if e != nil {
			return e
		}
	} else {
		// Do not fall back to the main site's host for a reseller order.
		site, e := data.Client(ctx, r.data).ResellerSite.Query().Where(resellersite.ProfileID(o.SubsiteID), resellersite.StatusEQ(resellersite.StatusActive), resellersite.VerificationStatusEQ(resellersite.VerificationStatusVerified)).Order(ent.Desc(resellersite.FieldIsPrimary), ent.Asc(resellersite.FieldID)).First(ctx)
		if e != nil && !ent.IsNotFound(e) {
			return e
		}
		if site != nil {
			base = "https://" + site.Domain
		}
	}
	vars["fetch_url"] = ""
	u, err := url.Parse(strings.TrimSpace(base))
	if err == nil && (u.Scheme == "https" || u.Scheme == "http") && u.Host != "" && u.User == nil {
		u.Path, u.RawPath, u.RawQuery, u.Fragment = "/fetch", "", url.Values{"order_no": {o.OrderNo}}.Encode(), ""
		vars["fetch_url"] = u.String()
	}
	return nil
}

func defaultOrderTemplate(eventType, channel, locale string) (*ent.NotifyTemplate, error) {
	if channel == "email" && (eventType == "order.paid" || eventType == "order.delivered") {
		subject := "订单 {{.order_no}} 已支付"
		body := "<p>您的订单 {{.order_no}} 已支付，订单金额：{{.amount}}。</p>"
		if eventType == "order.delivered" {
			subject = "订单 {{.order_no}} 已完成交付"
			body = "<p>您的订单 {{.order_no}} 已完成交付。</p>"
		}
		body += "<p><a href=\"{{.fetch_url}}\">查看订单与交付结果</a></p><p>请使用下单邮箱验证、查询密码或订单所属账户查看。卡密仅在授权后的店铺页面显示。</p>"
		if locale == "en" {
			subject = "Order {{.order_no}} payment received"
			body = "<p>Payment for order {{.order_no}} has been received. Order amount: {{.amount}}.</p>"
			if eventType == "order.delivered" {
				subject = "Order {{.order_no}} delivery completed"
				body = "<p>Delivery for order {{.order_no}} is complete.</p>"
			}
			body += "<p><a href=\"{{.fetch_url}}\">View your order and delivery results</a></p><p>Verify the email used at checkout, enter your query password, or sign in to the account that owns the order. Delivery content is only shown on the authorized store page.</p>"
		}
		return &ent.NotifyTemplate{SubjectTpl: subject, BodyTpl: body, Enabled: true}, nil
	}
	if eventType == "order.shipped" {
		return &ent.NotifyTemplate{SubjectTpl: "订单 {{.order_no}} 的包裹已寄出", BodyTpl: "快递公司：{{.carrier}}；快递单号：{{.tracking_no}}。请在订单详情查看配送信息。", Enabled: true}, nil
	}
	if eventType == "order.received" {
		return &ent.NotifyTemplate{SubjectTpl: "订单 {{.order_no}} 已确认收货", BodyTpl: "实体商品已确认收货，请在订单详情查看配送及售后记录。", Enabled: true}, nil
	}
	return nil, ErrNotFound
}
