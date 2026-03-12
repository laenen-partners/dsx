package e2e_test

import (
	"testing"
	"time"

	pw "github.com/playwright-community/playwright-go"
)

func TestStreamPage_CounterRenders(t *testing.T) {
	page := newPage(t)
	if _, err := page.Goto(baseURL+"/components/stream", pw.PageGotoOptions{
		WaitUntil: pw.WaitUntilStateDomcontentloaded,
	}); err != nil {
		t.Fatalf("goto: %v", err)
	}

	loc := page.Locator("#stream-counter-value")
	if err := loc.WaitFor(pw.LocatorWaitForOptions{
		Timeout: pw.Float(5000),
	}); err != nil {
		t.Fatalf("waiting for counter: %v", err)
	}

	text, err := loc.TextContent()
	if err != nil {
		t.Fatalf("text content: %v", err)
	}

	// After SSE init, the counter should be a number (not the "—" placeholder)
	if text == "—" || text == "" {
		t.Errorf("counter should have loaded a numeric value, got %q", text)
	}
}

func TestStreamPage_IncrementUpdatesCounter(t *testing.T) {
	page := newPage(t)
	if _, err := page.Goto(baseURL+"/components/stream", pw.PageGotoOptions{
		WaitUntil: pw.WaitUntilStateDomcontentloaded,
	}); err != nil {
		t.Fatalf("goto: %v", err)
	}

	counter := page.Locator("#stream-counter-value")
	if err := counter.WaitFor(pw.LocatorWaitForOptions{
		Timeout: pw.Float(5000),
	}); err != nil {
		t.Fatalf("waiting for counter: %v", err)
	}

	// Wait for initial value to load (not placeholder)
	time.Sleep(1 * time.Second)

	before, err := counter.TextContent()
	if err != nil {
		t.Fatalf("text content before: %v", err)
	}

	// Click the "+" button
	plusBtn := page.Locator("button:has-text(\"+\")")
	if err := plusBtn.Click(); err != nil {
		t.Fatalf("click +: %v", err)
	}

	// Wait for the counter to change
	time.Sleep(2 * time.Second)

	after, err := counter.TextContent()
	if err != nil {
		t.Fatalf("text content after: %v", err)
	}

	if before == after {
		t.Errorf("counter did not change after increment: before=%q after=%q", before, after)
	}
}

func TestStreamPage_DecrementUpdatesCounter(t *testing.T) {
	page := newPage(t)
	if _, err := page.Goto(baseURL+"/components/stream", pw.PageGotoOptions{
		WaitUntil: pw.WaitUntilStateDomcontentloaded,
	}); err != nil {
		t.Fatalf("goto: %v", err)
	}

	counter := page.Locator("#stream-counter-value")
	if err := counter.WaitFor(pw.LocatorWaitForOptions{
		Timeout: pw.Float(5000),
	}); err != nil {
		t.Fatalf("waiting for counter: %v", err)
	}

	time.Sleep(1 * time.Second)

	before, err := counter.TextContent()
	if err != nil {
		t.Fatalf("text content before: %v", err)
	}

	// Click the "−" button (minus sign)
	minusBtn := page.Locator("button:has-text(\"−\")")
	if err := minusBtn.Click(); err != nil {
		t.Fatalf("click −: %v", err)
	}

	time.Sleep(2 * time.Second)

	after, err := counter.TextContent()
	if err != nil {
		t.Fatalf("text content after: %v", err)
	}

	if before == after {
		t.Errorf("counter did not change after decrement: before=%q after=%q", before, after)
	}
}

func TestStreamPage_ResetCounter(t *testing.T) {
	page := newPage(t)
	if _, err := page.Goto(baseURL+"/components/stream", pw.PageGotoOptions{
		WaitUntil: pw.WaitUntilStateDomcontentloaded,
	}); err != nil {
		t.Fatalf("goto: %v", err)
	}

	counter := page.Locator("#stream-counter-value")
	if err := counter.WaitFor(pw.LocatorWaitForOptions{
		Timeout: pw.Float(5000),
	}); err != nil {
		t.Fatalf("waiting for counter: %v", err)
	}

	time.Sleep(1 * time.Second)

	// Increment first to ensure non-zero
	plusBtn := page.Locator("button:has-text(\"+\")")
	if err := plusBtn.Click(); err != nil {
		t.Fatalf("click +: %v", err)
	}
	time.Sleep(1 * time.Second)

	// Click Reset
	resetBtn := page.Locator("button:has-text(\"Reset\")")
	if err := resetBtn.Click(); err != nil {
		t.Fatalf("click Reset: %v", err)
	}

	time.Sleep(2 * time.Second)

	after, err := counter.TextContent()
	if err != nil {
		t.Fatalf("text content after reset: %v", err)
	}

	if after != "0" {
		t.Errorf("counter should be 0 after reset, got %q", after)
	}
}

func TestStreamPage_CrossTabSync(t *testing.T) {
	page1 := newPage(t)
	page2 := newPage(t)

	// Open stream page in both tabs
	if _, err := page1.Goto(baseURL+"/components/stream", pw.PageGotoOptions{
		WaitUntil: pw.WaitUntilStateDomcontentloaded,
	}); err != nil {
		t.Fatalf("page1 goto: %v", err)
	}
	if _, err := page2.Goto(baseURL+"/components/stream", pw.PageGotoOptions{
		WaitUntil: pw.WaitUntilStateDomcontentloaded,
	}); err != nil {
		t.Fatalf("page2 goto: %v", err)
	}

	counter1 := page1.Locator("#stream-counter-value")
	counter2 := page2.Locator("#stream-counter-value")

	// Wait for both to load
	if err := counter1.WaitFor(pw.LocatorWaitForOptions{Timeout: pw.Float(5000)}); err != nil {
		t.Fatalf("page1 waiting for counter: %v", err)
	}
	if err := counter2.WaitFor(pw.LocatorWaitForOptions{Timeout: pw.Float(5000)}); err != nil {
		t.Fatalf("page2 waiting for counter: %v", err)
	}
	time.Sleep(1 * time.Second)

	// Reset to known state from page1
	resetBtn := page1.Locator("button:has-text(\"Reset\")")
	if err := resetBtn.Click(); err != nil {
		t.Fatalf("page1 click Reset: %v", err)
	}
	time.Sleep(2 * time.Second)

	// Increment on page1
	plusBtn := page1.Locator("button:has-text(\"+\")")
	if err := plusBtn.Click(); err != nil {
		t.Fatalf("page1 click +: %v", err)
	}

	// Wait for page2 to receive update
	time.Sleep(2 * time.Second)

	// Check page2 updated
	text2, err := counter2.TextContent()
	if err != nil {
		t.Fatalf("page2 text content: %v", err)
	}

	if text2 != "1" {
		t.Errorf("page2 counter should be 1 after page1 increment, got %q", text2)
	}
}
