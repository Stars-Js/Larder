package api

import (
	"strings"
	"testing"
)

func TestBadgeSVGContainsRightAlignedLabel(t *testing.T) {
	svg := badgeSVG("英语六级词汇（1.pdf 实测）")
	if !strings.Contains(svg, "<svg") || !strings.Contains(svg, "text-anchor=\"end\"") {
		t.Fatalf("badge SVG missing svg/right alignment: %s", svg)
	}
	if !strings.Contains(svg, "Larder · 英语六级词汇（1.pdf 实测）") {
		t.Fatalf("badge SVG missing KB name: %s", svg)
	}
}

func TestBadgeSVGEscapesXML(t *testing.T) {
	svg := badgeSVG(`英语 <六级> & "实测"`)
	if strings.Contains(svg, "<六级>") {
		t.Fatalf("badge SVG should escape XML: %s", svg)
	}
}
