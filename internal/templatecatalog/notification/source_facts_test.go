package notification

import "testing"

func TestSourceFactsCatalogAndRendering(t *testing.T) {
	for _, trigger := range []string{TriggerDownloadSubmitted, TriggerDownloadFailed, TriggerFileOperationCompleted, TriggerFileOperationFailed, TriggerUnknownFilesImported, TriggerReleaseDiscovered, TriggerReplacementApplied} {
		t.Run(trigger, func(t *testing.T) {
			variables, _ := Variables(trigger)
			names := map[string]bool{}
			for _, variable := range variables {
				names[variable.Name] = true
			}
			for _, name := range []string{"release.src", "release.site", ReleaseLinkVariable, DownloadLinkVariable, AniBTLinkVariable, MikanLinkVariable} {
				if !names[name] {
					t.Errorf("missing %s", name)
				}
			}
			for _, site := range []any{nil, "Sample Tracker"} {
				message, ok := RenderMessage(trigger, "Sample", "开头\n{{showIf(release.site)}}来源：{{release.site}}\n结尾", map[string]any{"release.site": site})
				want := "开头\n结尾"
				if site != nil {
					want = "开头\n来源：Sample Tracker\n结尾"
				}
				if !ok || !message.Valid() || message.Body != want {
					t.Fatalf("source visibility: %+v want=%q", message, want)
				}
			}
			facts := Sample(trigger)
			if LinkValue(facts, ReleaseLinkVariable) == "" || LinkValue(facts, AniBTLinkVariable) == "" || LinkValue(facts, MikanLinkVariable) == "" {
				t.Fatalf("inconsistent source sample: %v", facts)
			}
			for _, url := range []string{"https://tracker.example.test/details/1", "", "https://tracker.example.test/details/1?passkey=private", "https://user@tracker.example.test/details/1"} {
				facts[ReleaseLinkVariable] = url
				expected := SafeExternalURL(url, false)
				message, ok := RenderMessage(trigger, "Sample", "{{release.link}}", facts)
				if !ok || !message.Valid() || message.Body != expected {
					t.Fatalf("direct source link: %+v", message)
				}
				attachments, ok := SemanticAttachments(trigger, "Sample", "{{link(release.link, '发布页面')}}", facts)
				if !ok || (expected == "" && len(attachments.Links) != 0) || (expected != "" && (len(attachments.Links) != 1 || attachments.Links[0].URL != expected)) {
					t.Fatalf("source attachment: %+v", attachments)
				}
			}
		})
	}
	for _, trigger := range []string{TriggerSubscriptionChanged, TriggerFileDisplaced, TriggerReplacementDeletionFailed} {
		for _, body := range []string{"{{release.src}}", "{{release.site}}", "{{release.link}}", "{{download.link}}", "{{showIf(release.site)}}来源", "{{link(release.link, '发布页面')}}"} {
			if errors, _ := Compile(trigger, "Sample", body); len(errors) == 0 {
				t.Errorf("%s compiled unavailable source: %s", trigger, body)
			}
		}
		variables, _ := Variables(trigger)
		for _, v := range variables {
			if v.Name == "release.src" || v.Name == "release.site" || v.Name == ReleaseLinkVariable || v.Name == DownloadLinkVariable {
				t.Errorf("%s exposes ambiguous source %s", trigger, v.Name)
			}
		}
	}
}

func TestSourceLinksRejectMalformedQueriesAndEmptyProviderIDs(t *testing.T) {
	for _, raw := range []string{
		"https://tracker.example.test/details/1?passkey=private;other=value",
		"https://tracker.example.test/details/1?token=%zz",
		"https://tracker.example.test/details/1?%74oken=private",
	} {
		if got := SafeExternalURL(raw, false); got != "" {
			t.Fatalf("unsafe query accepted: %q", got)
		}
		for _, key := range []string{ReleaseLinkVariable, DownloadLinkVariable} {
			message, ok := RenderMessage(TriggerDownloadSubmitted, "Sample", "{{"+key+"}}", map[string]any{key: raw})
			if !ok || !message.Valid() || message.Body != "" {
				t.Fatalf("unsafe direct link: %+v", message)
			}
		}
	}
	for key, page := range map[string]string{AniBTLinkVariable: "https://anibt.net/release/", MikanLinkVariable: "https://mikanani.me/Home/Episode/"} {
		if got := LinkValue(map[string]any{key: page}, key); got != "" {
			t.Fatalf("empty provider ID accepted: %q", got)
		}
	}
}

func TestMikanLinkAcceptsBothDeclaredHosts(t *testing.T) {
	for _, host := range []string{"mikanani.me", "mikanime.tv"} {
		page := "https://" + host + "/Home/Episode/sample"
		if got := LinkValue(map[string]any{MikanLinkVariable: page}, MikanLinkVariable); got != page {
			t.Fatalf("page=%q got=%q", page, got)
		}
	}
}
