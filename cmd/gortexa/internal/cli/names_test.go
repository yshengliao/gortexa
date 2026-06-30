package cli

import "testing"

func TestParseTargetValid(t *testing.T) {
	d, err := parseTarget("billing/v1", "Invoice")
	if err != nil {
		t.Fatal(err)
	}
	want := tmplData{
		Domain: "billing", Version: "v1", GoPkg: "billingv1",
		Entity: "Invoice", Snake: "invoice", Plural: "Invoices", PluralSnake: "invoices",
	}
	if d != want {
		t.Errorf("parseTarget = %+v, want %+v", d, want)
	}
}

func TestParseTargetDefaultsEntityFromDomain(t *testing.T) {
	d, err := parseTarget("order/v2", "")
	if err != nil {
		t.Fatal(err)
	}
	if d.Entity != "Order" || d.GoPkg != "orderv2" {
		t.Errorf("got entity=%q gopkg=%q, want Order/orderv2", d.Entity, d.GoPkg)
	}
}

func TestParseTargetInvalid(t *testing.T) {
	for _, tgt := range []string{"Billing/v1", "billing", "billing/1", "billing/vX", "bil-ling/v1"} {
		if _, err := parseTarget(tgt, ""); err == nil {
			t.Errorf("parseTarget(%q) expected error", tgt)
		}
	}
	if _, err := parseTarget("billing/v1", "invoice"); err == nil {
		t.Error("lowercase entity should error")
	}
}

func TestCamelToSnake(t *testing.T) {
	cases := map[string]string{"Invoice": "invoice", "InvoiceItem": "invoice_item", "Order": "order"}
	for in, want := range cases {
		if got := camelToSnake(in); got != want {
			t.Errorf("camelToSnake(%q) = %q, want %q", in, got, want)
		}
	}
}
