package e2e_test

import (
	"testing"

	pw "github.com/playwright-community/playwright-go"
)

func TestDrawerAdvancedPage_Renders(t *testing.T) {
	page := newPage(t)
	if _, err := page.Goto(baseURL+"/components/drawer-advanced", pw.PageGotoOptions{
		WaitUntil: pw.WaitUntilStateDomcontentloaded,
	}); err != nil {
		t.Fatalf("goto: %v", err)
	}

	heading := page.Locator("h1", pw.PageLocatorOptions{HasText: "Advanced Drawer"})
	if err := heading.WaitFor(pw.LocatorWaitForOptions{
		State: pw.WaitForSelectorStateVisible,
	}); err != nil {
		t.Fatalf("heading not visible: %v", err)
	}

	// Should have 6 project cards.
	cards := page.Locator(".card")
	count, err := cards.Count()
	if err != nil {
		t.Fatalf("count cards: %v", err)
	}
	// 6 project cards (How It Works uses Example component, not card)
	if count < 6 {
		t.Errorf("expected at least 6 cards, got %d", count)
	}
}

func TestDrawerAdvancedPage_CardOpensDrawer(t *testing.T) {
	page := newPage(t)
	if _, err := page.Goto(baseURL+"/components/drawer-advanced", pw.PageGotoOptions{
		WaitUntil: pw.WaitUntilStateNetworkidle,
	}); err != nil {
		t.Fatalf("goto: %v", err)
	}

	// Click the first project card body.
	firstCard := page.Locator(".card-body").First()
	if err := firstCard.Click(); err != nil {
		t.Fatalf("click card: %v", err)
	}

	// Drawer content should appear.
	drawerContent := page.Locator("#drawer-panel h2")
	if err := drawerContent.WaitFor(pw.LocatorWaitForOptions{
		State:   pw.WaitForSelectorStateVisible,
		Timeout: pw.Float(5000),
	}); err != nil {
		t.Fatalf("drawer content did not appear: %v", err)
	}

	// Close via close button.
	closeBtn := page.Locator("#drawer-panel button.btn-circle")
	if err := closeBtn.Click(); err != nil {
		t.Fatalf("click close: %v", err)
	}

	// Drawer content should disappear.
	if err := drawerContent.WaitFor(pw.LocatorWaitForOptions{
		State:   pw.WaitForSelectorStateHidden,
		Timeout: pw.Float(3000),
	}); err != nil {
		t.Fatalf("drawer did not close: %v", err)
	}
}
