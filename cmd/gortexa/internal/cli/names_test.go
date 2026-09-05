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

func TestProtoNamespace(t *testing.T) {
	for _, tc := range []struct{ module, want string }{
		{"github.com/acme/shop", "shop"},
		{"example.com/me/my-app", "myapp"},
		{"example.com/App2", "app2"},
		{"example.com/2fast", "fast"}, // a proto package component may not start with a digit
		{"example.com/---", "app"},    // nothing usable left
		{"shop", "shop"},              // no slash at all
	} {
		if got := protoNamespace(tc.module); got != tc.want {
			t.Errorf("protoNamespace(%q) = %q, want %q", tc.module, got, tc.want)
		}
	}
}

// TestTmplDataNamespaceAccessors pins the flat fallback: a project scaffolded
// before v0.28 has no manifest, and gen must keep generating the layout its
// existing services already use rather than splitting the project across two
// conventions.
func TestTmplDataNamespaceAccessors(t *testing.T) {
	flat := tmplData{Domain: "billing", Version: "v1"}
	if got := flat.ProtoPackage(); got != "billing.v1" {
		t.Errorf("flat ProtoPackage = %q, want billing.v1", got)
	}
	if got := flat.GenDir(); got != "billing/v1" {
		t.Errorf("flat GenDir = %q, want billing/v1", got)
	}
	ns := tmplData{Domain: "billing", Version: "v1", Namespace: "shop"}
	if got := ns.ProtoPackage(); got != "shop.billing.v1" {
		t.Errorf("namespaced ProtoPackage = %q, want shop.billing.v1", got)
	}
	if got := ns.GenDir(); got != "shop/billing/v1" {
		t.Errorf("namespaced GenDir = %q, want shop/billing/v1", got)
	}
}
