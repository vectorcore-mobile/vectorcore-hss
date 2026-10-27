package policy

import "testing"

// The values are what VoLTE (Rx) has always used; N5 must match them.
func TestIMSMediaQoSFor(t *testing.T) {
	for _, c := range []struct {
		name string
		t    IMSMediaType
		want IMSMediaQoS
		ok   bool
	}{
		{"audio", IMSMediaAudio, IMSMediaQoS{QCI: 1, GBR: true, DefaultBW: 128000, ARPPriority: 14, Precedence: 40}, true},
		{"video", IMSMediaVideo, IMSMediaQoS{QCI: 2, GBR: true, DefaultBW: 512000, ARPPriority: 11, Precedence: 30}, true},
		{"control", IMSMediaControl, IMSMediaQoS{}, false},
		{"other", IMSMediaOther, IMSMediaQoS{}, false},
	} {
		got, ok := IMSMediaQoSFor(c.t)
		if ok != c.ok || got != c.want {
			t.Errorf("%s: got %+v ok=%v, want %+v ok=%v", c.name, got, ok, c.want, c.ok)
		}
	}
}

func TestIsIMSSignalling(t *testing.T) {
	sig, rtcp, none := IMSFlowUsageAFSignalling, IMSFlowUsageRTCP, IMSFlowUsageNoInfo
	for _, c := range []struct {
		name   string
		t      IMSMediaType
		usages []IMSFlowUsage
		want   bool
	}{
		{"CONTROL", IMSMediaControl, nil, true},
		{"all AF_SIGNALLING, no media type", IMSMediaOther, []IMSFlowUsage{sig, sig}, true},
		{"audio, no sub-components", IMSMediaAudio, nil, false},
		{"audio with RTCP", IMSMediaAudio, []IMSFlowUsage{none, rtcp}, false},
		{"mixed signalling and media", IMSMediaAudio, []IMSFlowUsage{sig, none}, false},
	} {
		if got := IsIMSSignalling(c.t, c.usages); got != c.want {
			t.Errorf("%s: got %v, want %v", c.name, got, c.want)
		}
	}
}
