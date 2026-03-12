package e2e_test

import (
	"testing"

	pw "github.com/playwright-community/playwright-go"
)

func TestSSESDKPage_Renders(t *testing.T) {
	page := newPage(t)
	if _, err := page.Goto(baseURL+"/components/sse-sdk", pw.PageGotoOptions{
		WaitUntil: pw.WaitUntilStateDomcontentloaded,
	}); err != nil {
		t.Fatalf("goto: %v", err)
	}

	heading := page.Locator("h1", pw.PageLocatorOptions{HasText: "SSE SDK"})
	if err := heading.WaitFor(pw.LocatorWaitForOptions{
		State: pw.WaitForSelectorStateVisible,
	}); err != nil {
		t.Fatalf("heading not visible: %v", err)
	}

	// Should have example sections for each helper (uses Example component with tabs, not cards).
	examples := page.Locator("h2.text-xl")
	count, err := examples.Count()
	if err != nil {
		t.Fatalf("count examples: %v", err)
	}
	if count < 5 {
		t.Errorf("expected at least 5 example sections, got %d", count)
	}
}

func TestSSESDKPage_ModalOpens(t *testing.T) {
	page := newPage(t)
	if _, err := page.Goto(baseURL+"/components/sse-sdk", pw.PageGotoOptions{
		WaitUntil: pw.WaitUntilStateNetworkidle,
	}); err != nil {
		t.Fatalf("goto: %v", err)
	}

	// Click "Open Modal" button.
	btn := page.Locator("button", pw.PageLocatorOptions{HasText: "Open Modal"})
	if err := btn.Click(); err != nil {
		t.Fatalf("click open modal: %v", err)
	}

	// Modal content should appear.
	modal := page.Locator("#modal-panel h2", pw.PageLocatorOptions{HasText: "Modal Title"})
	if err := modal.WaitFor(pw.LocatorWaitForOptions{
		State:   pw.WaitForSelectorStateVisible,
		Timeout: pw.Float(5000),
	}); err != nil {
		t.Fatalf("modal content did not appear: %v", err)
	}

	// Close via close button.
	closeBtn := page.Locator("#modal-panel button.btn-circle")
	if err := closeBtn.Click(); err != nil {
		t.Fatalf("click close: %v", err)
	}

	// Modal should disappear.
	if err := modal.WaitFor(pw.LocatorWaitForOptions{
		State:   pw.WaitForSelectorStateHidden,
		Timeout: pw.Float(3000),
	}); err != nil {
		t.Fatalf("modal did not close: %v", err)
	}
}

func TestSSESDKPage_ConfirmDialog(t *testing.T) {
	page := newPage(t)
	if _, err := page.Goto(baseURL+"/components/sse-sdk", pw.PageGotoOptions{
		WaitUntil: pw.WaitUntilStateNetworkidle,
	}); err != nil {
		t.Fatalf("goto: %v", err)
	}

	// Click "Ask Confirmation".
	btn := page.Locator("button", pw.PageLocatorOptions{HasText: "Ask Confirmation"})
	if err := btn.Click(); err != nil {
		t.Fatalf("click ask confirmation: %v", err)
	}

	// Confirm dialog should appear.
	confirmTitle := page.Locator("#modal-panel h3", pw.PageLocatorOptions{HasText: "Confirm"})
	if err := confirmTitle.WaitFor(pw.LocatorWaitForOptions{
		State:   pw.WaitForSelectorStateVisible,
		Timeout: pw.Float(5000),
	}); err != nil {
		t.Fatalf("confirm dialog did not appear: %v", err)
	}

	// Cancel should close.
	cancelBtn := page.Locator("#modal-panel button", pw.PageLocatorOptions{HasText: "Cancel"})
	if err := cancelBtn.Click(); err != nil {
		t.Fatalf("click cancel: %v", err)
	}

	if err := confirmTitle.WaitFor(pw.LocatorWaitForOptions{
		State:   pw.WaitForSelectorStateHidden,
		Timeout: pw.Float(3000),
	}); err != nil {
		t.Fatalf("confirm dialog did not close: %v", err)
	}
}

func TestSSESDKPage_PatchContent(t *testing.T) {
	page := newPage(t)
	if _, err := page.Goto(baseURL+"/components/sse-sdk", pw.PageGotoOptions{
		WaitUntil: pw.WaitUntilStateNetworkidle,
	}); err != nil {
		t.Fatalf("goto: %v", err)
	}

	// Get initial content.
	target := page.Locator("#patch-target")
	initialText, err := target.TextContent()
	if err != nil {
		t.Fatalf("initial text: %v", err)
	}

	// Click "Patch Content".
	btn := page.Locator("button", pw.PageLocatorOptions{HasText: "Patch Content"})
	if err := btn.Click(); err != nil {
		t.Fatalf("click patch: %v", err)
	}

	// Content should change — wait for the patched text to appear.
	patched := page.Locator("#patch-target", pw.PageLocatorOptions{HasText: "patched from the server"})
	if err := patched.WaitFor(pw.LocatorWaitForOptions{
		State:   pw.WaitForSelectorStateVisible,
		Timeout: pw.Float(5000),
	}); err != nil {
		t.Fatalf("patched content did not appear: %v", err)
	}

	newText, err := target.TextContent()
	if err != nil {
		t.Fatalf("new text: %v", err)
	}
	if newText == initialText {
		t.Error("content did not change after patch")
	}
}
