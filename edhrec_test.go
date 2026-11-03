package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestSanitizeCardName(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{
			name:  "simple name",
			input: "Sol Ring",
			want:  "sol-ring",
		},
		{
			name:  "name with comma",
			input: "Atraxa, Praetors' Voice",
			want:  "atraxa-praetors-voice",
		},
		{
			name:  "name with apostrophe",
			input: "Jace's Ingenuity",
			want:  "jaces-ingenuity",
		},
		{
			name:  "name with special characters",
			input: "Teferi, Hero of Dominaria",
			want:  "teferi-hero-of-dominaria",
		},
		{
			name:  "name with multiple spaces",
			input: "Black   Lotus",
			want:  "black-lotus",
		},
		{
			name:  "name with hyphens",
			input: "Will-o'-the-Wisp",
			want:  "will-o-the-wisp",
		},
		{
			name:  "already sanitized",
			input: "lightning-bolt",
			want:  "lightning-bolt",
		},
		{
			name:  "all caps",
			input: "TEFERI",
			want:  "teferi",
		},
		{
			name:  "with numbers",
			input: "Mox Opal 2",
			want:  "mox-opal-2",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := SanitizeCardName(tt.input)
			if got != tt.want {
				t.Errorf("SanitizeCardName() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestGetCommanderRecommendations(t *testing.T) {
	tests := []struct {
		name          string
		commanderName string
		filter        EDHRECRecFilter
		mockResponse  EDHRECResponse
		mockStatus    int
		wantErr       bool
		checkURL      bool
		expectedURL   string
	}{
		{
			name:          "successful request",
			commanderName: "Atraxa, Praetors' Voice",
			mockResponse: EDHRECResponse{
				Container: EDHRECContainer{
					JSONDict: EDHRECData{
						Card: EDHRECCardInfo{
							Name:      "Atraxa, Praetors' Voice",
							Sanitized: "atraxa-praetors-voice",
							ColorID:   []string{"W", "U", "B", "G"},
							NumDecks:  50000,
						},
						CardLists: []EDHRECCardList{
							{
								Header: "High Synergy Cards",
								Tag:    "highsynergy",
								CardViews: []EDHRECCardView{
									{
										Name:      "Doubling Season",
										Sanitized: "doubling-season",
										NumDecks:  25000,
										Synergy:   0.35,
									},
								},
							},
						},
					},
				},
			},
			mockStatus:  http.StatusOK,
			wantErr:     false,
			checkURL:    true,
			expectedURL: "/commanders/atraxa-praetors-voice.json",
		},
		{
			name:          "404 not found",
			commanderName: "Nonexistent Commander",
			mockStatus:    http.StatusNotFound,
			wantErr:       true,
		},
		{
			name:          "500 server error",
			commanderName: "Atraxa",
			mockStatus:    http.StatusInternalServerError,
			wantErr:       true,
		},
		{
			name:          "unfiltered URL",
			commanderName: "Atraxa, Praetors' Voice",
			filter:        EDHRECRecFilter{},
			mockStatus:    http.StatusOK,
			checkURL:      true,
			expectedURL:   "/commanders/atraxa-praetors-voice.json",
		},
		{
			name:          "theme filter composes URL",
			commanderName: "Atraxa, Praetors' Voice",
			filter:        EDHRECRecFilter{Theme: "infect"},
			mockStatus:    http.StatusOK,
			checkURL:      true,
			expectedURL:   "/commanders/atraxa-praetors-voice/infect.json",
		},
		{
			name:          "price tier filter composes URL",
			commanderName: "Atraxa, Praetors' Voice",
			filter:        EDHRECRecFilter{PriceTier: "budget"},
			mockStatus:    http.StatusOK,
			checkURL:      true,
			expectedURL:   "/commanders/atraxa-praetors-voice/budget.json",
		},
		{
			name:          "theme and price tier compose in theme-then-tier order",
			commanderName: "Atraxa, Praetors' Voice",
			filter:        EDHRECRecFilter{Theme: "infect", PriceTier: "budget"},
			mockStatus:    http.StatusOK,
			checkURL:      true,
			expectedURL:   "/commanders/atraxa-praetors-voice/infect/budget.json",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if tt.checkURL && !strings.HasSuffix(r.URL.Path, tt.expectedURL) {
					t.Errorf("Request URL = %v, want suffix %v", r.URL.Path, tt.expectedURL)
				}

				w.WriteHeader(tt.mockStatus)
				if tt.mockStatus == http.StatusOK {
					_ = json.NewEncoder(w).Encode(tt.mockResponse)
				}
			}))
			defer server.Close()

			ctx := context.Background()
			got, err := getCommanderPageWithURL(ctx, tt.commanderName, tt.filter, server.URL)

			if (err != nil) != tt.wantErr {
				t.Errorf("getCommanderPageWithURL() error = %v, wantErr %v", err, tt.wantErr)
				return
			}

			if !tt.wantErr && got != nil {
				if got.Container.JSONDict.Card.Name != tt.mockResponse.Container.JSONDict.Card.Name {
					t.Errorf("getCommanderPageWithURL() card name = %v, want %v",
						got.Container.JSONDict.Card.Name, tt.mockResponse.Container.JSONDict.Card.Name)
				}
			}
		})
	}
}

func TestGetSetCardsWithURL(t *testing.T) {
	tests := []struct {
		name        string
		setCode     string
		mockStatus  int
		wantErr     bool
		expectedURL string
	}{
		{
			name:        "lowercase code",
			setCode:     "rna",
			mockStatus:  http.StatusOK,
			expectedURL: "/sets/rna.json",
		},
		{
			name:        "uppercase code is lowercased",
			setCode:     "RNA",
			mockStatus:  http.StatusOK,
			expectedURL: "/sets/rna.json",
		},
		{
			name:        "whitespace is trimmed",
			setCode:     " rna ",
			mockStatus:  http.StatusOK,
			expectedURL: "/sets/rna.json",
		},
		{
			name:       "403 forbidden yields an error",
			setCode:    "notaset",
			mockStatus: http.StatusForbidden,
			wantErr:    true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if tt.expectedURL != "" && !strings.HasSuffix(r.URL.Path, tt.expectedURL) {
					t.Errorf("Request URL = %v, want suffix %v", r.URL.Path, tt.expectedURL)
				}

				w.WriteHeader(tt.mockStatus)
				if tt.mockStatus == http.StatusOK {
					_ = json.NewEncoder(w).Encode(EDHRECResponse{Header: "Ravnica Allegiance"})
				}
			}))
			defer server.Close()

			_, err := getSetCardsWithURL(context.Background(), tt.setCode, server.URL)
			if (err != nil) != tt.wantErr {
				t.Errorf("getSetCardsWithURL() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestGetCombosForColors(t *testing.T) {
	tests := []struct {
		name         string
		colors       string
		mockResponse EDHRECComboResponse
		mockStatus   int
		wantErr      bool
	}{
		{
			name:   "colorless combos",
			colors: "colorless",
			mockResponse: EDHRECComboResponse{
				Container: EDHRECComboContainer{
					JSONDict: EDHRECComboData{
						CardLists: []EDHRECComboList{
							{
								Header: "Basalt Monolith + Forsaken Monument",
								CardViews: []EDHRECCardView{
									{Name: "Basalt Monolith"},
									{Name: "Forsaken Monument"},
								},
								Combo: &EDHRECCombo{
									ComboID: "combo-1",
									Results: []string{"Infinite colorless mana"},
								},
							},
						},
					},
				},
			},
			mockStatus: http.StatusOK,
			wantErr:    false,
		},
		{
			name:       "404 not found",
			colors:     "invalid",
			mockStatus: http.StatusNotFound,
			wantErr:    true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tt.mockStatus)
				if tt.mockStatus == http.StatusOK {
					_ = json.NewEncoder(w).Encode(tt.mockResponse)
				}
			}))
			defer server.Close()

			ctx := context.Background()
			got, err := getCombosForColorsWithURL(ctx, tt.colors, server.URL)

			if (err != nil) != tt.wantErr {
				t.Errorf("GetCombosForColors() error = %v, wantErr %v", err, tt.wantErr)
				return
			}

			if !tt.wantErr && got != nil {
				if len(got.CardLists) != len(tt.mockResponse.Container.JSONDict.CardLists) {
					t.Errorf("GetCombosForColors() combo count = %v, want %v",
						len(got.CardLists), len(tt.mockResponse.Container.JSONDict.CardLists))
				}
			}
		})
	}
}

func TestFormatCommanderRecsForDisplay(t *testing.T) {
	page := &EDHRECResponse{
		Container: EDHRECContainer{
			JSONDict: EDHRECData{
				Card: EDHRECCardInfo{
					Name:     "Test Commander",
					ColorID:  []string{"W", "U"},
					NumDecks: 1000,
				},
				CardLists: []EDHRECCardList{
					{
						Header: "High Synergy Cards",
						CardViews: []EDHRECCardView{
							{
								Name:     "Card 1",
								NumDecks: 500,
								Synergy:  0.35,
								Salt:     1.5,
							},
							{
								Name:     "Card 2",
								NumDecks: 400,
								Synergy:  0.25,
							},
						},
					},
				},
			},
		},
	}

	tests := []struct {
		name          string
		page          *EDHRECResponse
		limit         int
		wantContains  []string
		wantCardCount int
	}{
		{
			name:  "with limit",
			page:  page,
			limit: 1,
			wantContains: []string{
				"Test Commander",
				"High Synergy Cards",
				"Card 1",
				"Synergy:",
				"Salt Score:",
			},
			wantCardCount: 1,
		},
		{
			name:  "without limit",
			page:  page,
			limit: 0,
			wantContains: []string{
				"Test Commander",
				"Card 1",
				"Card 2",
			},
			wantCardCount: 2,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := FormatCommanderRecsForDisplay(tt.page, EDHRECRecFilter{}, tt.limit)

			for _, want := range tt.wantContains {
				if !strings.Contains(got, want) {
					t.Errorf("FormatCommanderRecsForDisplay() missing %q in output", want)
				}
			}

			// Check if the correct number of cards are shown
			if tt.limit > 0 {
				card2Count := strings.Count(got, "Card 2")
				if tt.wantCardCount == 1 && card2Count > 0 {
					t.Error("FormatCommanderRecsForDisplay() should limit cards but found Card 2")
				}
			}
		})
	}
}

func TestFormatCommanderRecsForDisplayDeckStats(t *testing.T) {
	t.Run("nonzero total decks shows percentage", func(t *testing.T) {
		page := &EDHRECResponse{
			Container: EDHRECContainer{
				JSONDict: EDHRECData{
					Card: EDHRECCardInfo{Name: "Test Commander", NumDecks: 1000},
					CardLists: []EDHRECCardList{
						{Header: "Top Cards", CardViews: []EDHRECCardView{{Name: "Sol Ring", NumDecks: 500}}},
					},
				},
			},
		}
		got := FormatCommanderRecsForDisplay(page, EDHRECRecFilter{}, 10)
		if !strings.Contains(got, "Decks: 500 of 1000 (50.0%)") {
			t.Errorf("expected 50.0%% deck stat, got:\n%s", got)
		}
		if strings.Contains(got, "NaN") {
			t.Errorf("output must never contain NaN, got:\n%s", got)
		}
	})

	t.Run("zero total decks shows raw count only", func(t *testing.T) {
		page := &EDHRECResponse{
			Container: EDHRECContainer{
				JSONDict: EDHRECData{
					Card: EDHRECCardInfo{Name: "Test Commander", NumDecks: 0},
					CardLists: []EDHRECCardList{
						{Header: "Top Cards", CardViews: []EDHRECCardView{{Name: "Sol Ring", NumDecks: 500}}},
					},
				},
			},
		}
		got := FormatCommanderRecsForDisplay(page, EDHRECRecFilter{}, 10)
		if !strings.Contains(got, "Decks: 500") {
			t.Errorf("expected raw deck count, got:\n%s", got)
		}
		if strings.Contains(got, "NaN") || strings.Contains(got, "%") {
			t.Errorf("zero denominator must not print a percentage, got:\n%s", got)
		}
	})
}

func TestFormatCommanderRecsForDisplayThemes(t *testing.T) {
	tags := make([]EDHRECTagCount, 20)
	for i := range tags {
		tags[i] = EDHRECTagCount{
			Slug: fmt.Sprintf("theme-%02d", i), Value: fmt.Sprintf("Theme %02d", i), Count: 100 - i,
		}
	}

	page := &EDHRECResponse{
		Header:    "Test Commander (Commander)",
		TagCounts: tags,
		Container: EDHRECContainer{
			JSONDict: EDHRECData{Card: EDHRECCardInfo{Name: "Test Commander"}},
		},
	}

	t.Run("unfiltered shows available themes", func(t *testing.T) {
		got := FormatCommanderRecsForDisplay(page, EDHRECRecFilter{}, 10)
		if !strings.Contains(got, "## Available Themes") {
			t.Error("expected Available Themes section")
		}
		if !strings.Contains(got, "theme-00") {
			t.Error("expected highest-count theme slug")
		}
		if !strings.Contains(got, "and 5 more themes") {
			t.Errorf("expected omitted-theme count, got:\n%s", got)
		}
		if strings.Contains(got, "theme-15") {
			t.Error("16th theme slug should not be listed")
		}
	})

	t.Run("filtered suppresses available themes", func(t *testing.T) {
		got := FormatCommanderRecsForDisplay(page, EDHRECRecFilter{Theme: "infect"}, 10)
		if strings.Contains(got, "## Available Themes") {
			t.Error("Available Themes section should be suppressed when a filter is active")
		}
	})
}

func TestFormatCommanderRecsForDisplayHeader(t *testing.T) {
	t.Run("uses page header when present", func(t *testing.T) {
		page := &EDHRECResponse{
			Header: "X (Commander) - Budget Infect",
			Container: EDHRECContainer{
				JSONDict: EDHRECData{Card: EDHRECCardInfo{Name: "X"}},
			},
		}
		got := FormatCommanderRecsForDisplay(page, EDHRECRecFilter{Theme: "infect", PriceTier: "budget"}, 10)
		if !strings.Contains(got, "X (Commander) - Budget Infect") {
			t.Errorf("expected page header in title, got:\n%s", got)
		}
	})

	t.Run("falls back to card name when header is empty", func(t *testing.T) {
		page := &EDHRECResponse{
			Container: EDHRECContainer{
				JSONDict: EDHRECData{Card: EDHRECCardInfo{Name: "Fallback Commander"}},
			},
		}
		got := FormatCommanderRecsForDisplay(page, EDHRECRecFilter{}, 10)
		if !strings.Contains(got, "Fallback Commander") {
			t.Errorf("expected card name fallback in title, got:\n%s", got)
		}
	})
}

func TestFormatSetCardsForDisplay(t *testing.T) {
	t.Run("commander and card views", func(t *testing.T) {
		page := &EDHRECResponse{
			Header: "Ravnica Allegiance",
			Container: EDHRECContainer{
				JSONDict: EDHRECData{
					CardLists: []EDHRECCardList{
						{
							Header:    "Commanders",
							CardViews: []EDHRECCardView{{Name: "Teysa Karlov", NumDecks: 21469, PotentialDecks: 0}},
						},
						{
							Header: "Cards",
							CardViews: []EDHRECCardView{
								{Name: "Smothering Tithe", NumDecks: 500, PotentialDecks: 2000},
							},
						},
					},
				},
			},
		}
		got := FormatSetCardsForDisplay(page, "rna", 25)
		if !strings.Contains(got, "(rna)") {
			t.Errorf("expected set code in title, got:\n%s", got)
		}
		if strings.Contains(got, "Decks: 21469 of") || !strings.Contains(got, "Decks: 21469\n") {
			t.Errorf("commander view should show a bare deck count, got:\n%s", got)
		}
		if !strings.Contains(got, "Decks: 500 of 2000 (25.0%)") {
			t.Errorf("card view should show a percentage, got:\n%s", got)
		}
	})

	t.Run("truncation and unlimited", func(t *testing.T) {
		page := &EDHRECResponse{
			Container: EDHRECContainer{
				JSONDict: EDHRECData{
					CardLists: []EDHRECCardList{
						{
							Header: "Cards",
							CardViews: []EDHRECCardView{
								{Name: "Card One"},
								{Name: "Card Two"},
								{Name: "Card Three"},
							},
						},
					},
				},
			},
		}

		limited := FormatSetCardsForDisplay(page, "rna", 1)
		if !strings.Contains(limited, "and 2 more cards") {
			t.Errorf("expected truncation suffix, got:\n%s", limited)
		}

		unlimited := FormatSetCardsForDisplay(page, "rna", 0)
		for _, want := range []string{"Card One", "Card Two", "Card Three"} {
			if !strings.Contains(unlimited, want) {
				t.Errorf("limit 0 should show all cards, missing %q in:\n%s", want, unlimited)
			}
		}
		if strings.Contains(unlimited, "more cards") {
			t.Errorf("limit 0 should not truncate, got:\n%s", unlimited)
		}
	})
}

func TestFormatCombosForDisplay(t *testing.T) {
	data := &EDHRECComboData{
		CardLists: []EDHRECComboList{
			{
				Header: "Card A + Card B (1000 decks)",
				CardViews: []EDHRECCardView{
					{Name: "Card A"},
					{Name: "Card B"},
				},
				Combo: &EDHRECCombo{
					ComboID: "123-456",
					Results: []string{"Win the game", "Infinite mana"},
				},
			},
			{
				Header: "Card C + Card D (500 decks)",
				CardViews: []EDHRECCardView{
					{Name: "Card C"},
					{Name: "Card D"},
				},
				Combo: &EDHRECCombo{
					ComboID: "789-012",
					Results: []string{"Infinite tokens"},
				},
			},
		},
	}

	tests := []struct {
		name         string
		data         *EDHRECComboData
		limit        int
		wantContains []string
	}{
		{
			name:  "with limit",
			data:  data,
			limit: 1,
			wantContains: []string{
				"Popular Combos",
				"Card A + Card B",
				"1000 decks",
				"Win the game",
			},
		},
		{
			name:  "without limit",
			data:  data,
			limit: 0,
			wantContains: []string{
				"Card A + Card B",
				"Card C + Card D",
				"500 decks",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := FormatCombosForDisplay(tt.data, tt.limit)

			for _, want := range tt.wantContains {
				if !strings.Contains(got, want) {
					t.Errorf("FormatCombosForDisplay() missing %q in output", want)
				}
			}

			if tt.limit == 1 && strings.Contains(got, "Card C + Card D") {
				t.Error("FormatCombosForDisplay() should limit combos but found second combo")
			}
		})
	}
}

func TestGetTopPage(t *testing.T) {
	tests := []struct {
		name       string
		kind       string
		period     string
		mockStatus int
		wantPath   string
		wantErr    bool
	}{
		{
			name:       "commanders week",
			kind:       topKindCommanders,
			period:     topPeriodWeek,
			mockStatus: http.StatusOK,
			wantPath:   "/commanders/week.json",
		},
		{
			name:       "cards month",
			kind:       topKindCards,
			period:     topPeriodMonth,
			mockStatus: http.StatusOK,
			wantPath:   "/top/month.json",
		},
		{
			name:    "unknown kind",
			kind:    "lands",
			period:  topPeriodWeek,
			wantErr: true,
		},
		{
			name:       "403 forbidden",
			kind:       topKindCards,
			period:     "decade",
			mockStatus: http.StatusForbidden,
			wantPath:   "/top/decade.json",
			wantErr:    true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var gotPath string
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				gotPath = r.URL.Path
				w.WriteHeader(tt.mockStatus)
				if tt.mockStatus == http.StatusOK {
					_ = json.NewEncoder(w).Encode(EDHRECResponse{Header: "Top Commanders"})
				}
			}))
			defer server.Close()

			page, err := getTopPageWithURL(context.Background(), tt.kind, tt.period, server.URL)
			if (err != nil) != tt.wantErr {
				t.Fatalf("getTopPageWithURL() error = %v, wantErr %v", err, tt.wantErr)
			}
			if gotPath != tt.wantPath {
				t.Errorf("requested path %q, want %q", gotPath, tt.wantPath)
			}
			if !tt.wantErr && page.Header != "Top Commanders" {
				t.Errorf("header = %q, want %q", page.Header, "Top Commanders")
			}
		})
	}
}

func TestFormatTopForDisplay(t *testing.T) {
	page := &EDHRECResponse{
		Header: "Top Cards (Past Week)",
		Container: EDHRECContainer{
			JSONDict: EDHRECData{
				CardLists: []EDHRECCardList{
					{Header: "Empty", CardViews: []EDHRECCardView{}},
					{
						Header: "Past Week",
						CardViews: []EDHRECCardView{
							{Name: "Sol Ring", NumDecks: 230833, PotentialDecks: 275261},
							{Name: "Jace, Multiverse Architect", NumDecks: 2986},
							{Name: "Arcane Signet", NumDecks: 193741, PotentialDecks: 275261},
						},
					},
				},
			},
		},
	}

	t.Run("limit, percentages and raw counts", func(t *testing.T) {
		got := FormatTopForDisplay(page, topKindCards, topPeriodWeek, 2)
		for _, want := range []string{
			"# EDHREC Top Cards (Past Week)",
			"## Past Week",
			"1. **Sol Ring**",
			"230833 of 275261 (83.9%)",
			"2. **Jace, Multiverse Architect**",
			"   - Decks: 2986\n",
		} {
			if !strings.Contains(got, want) {
				t.Errorf("output missing %q:\n%s", want, got)
			}
		}
		if strings.Contains(got, "Arcane Signet") {
			t.Error("limit 2 should drop the third card")
		}
		if strings.Contains(got, "## Empty") {
			t.Error("empty card lists should be skipped")
		}
	})

	t.Run("zero limit shows everything", func(t *testing.T) {
		if got := FormatTopForDisplay(page, topKindCards, topPeriodWeek, 0); !strings.Contains(got, "Arcane Signet") {
			t.Errorf("limit 0 should show all cards:\n%s", got)
		}
	})

	t.Run("empty page", func(t *testing.T) {
		got := FormatTopForDisplay(&EDHRECResponse{}, topKindCommanders, topPeriodMonth, 10)
		if !strings.Contains(got, "# EDHREC Top commanders") || !strings.Contains(got, "No commanders ranked") {
			t.Errorf("unexpected empty-page output:\n%s", got)
		}
	})
}

func TestFormatCommanderRecsForDisplayEdgeCases(t *testing.T) {
	t.Run("empty card list skipped", func(t *testing.T) {
		page := &EDHRECResponse{
			Container: EDHRECContainer{
				JSONDict: EDHRECData{
					Card: EDHRECCardInfo{Name: "Test Commander", NumDecks: 100},
					CardLists: []EDHRECCardList{
						{Header: "Skipped Section", CardViews: []EDHRECCardView{}},
						{Header: "Non-Empty Section", CardViews: []EDHRECCardView{
							{Name: "Sol Ring", NumDecks: 90},
						}},
					},
				},
			},
		}
		got := FormatCommanderRecsForDisplay(page, EDHRECRecFilter{}, 10)
		if strings.Contains(got, "Skipped Section") {
			t.Error("FormatCommanderRecsForDisplay() should skip card lists with no cards")
		}
		if !strings.Contains(got, "Non-Empty Section") {
			t.Error("FormatCommanderRecsForDisplay() missing non-empty section")
		}
	})

	t.Run("zero synergy and zero salt omitted", func(t *testing.T) {
		page := &EDHRECResponse{
			Container: EDHRECContainer{
				JSONDict: EDHRECData{
					Card: EDHRECCardInfo{Name: "Test Commander", NumDecks: 100},
					CardLists: []EDHRECCardList{
						{Header: "Top Cards", CardViews: []EDHRECCardView{
							{Name: "Sol Ring", NumDecks: 90, Synergy: 0, Salt: 0},
						}},
					},
				},
			},
		}
		got := FormatCommanderRecsForDisplay(page, EDHRECRecFilter{}, 10)
		if strings.Contains(got, "Synergy:") {
			t.Error("FormatCommanderRecsForDisplay() should omit Synergy line when zero")
		}
		if strings.Contains(got, "Salt Score:") {
			t.Error("FormatCommanderRecsForDisplay() should omit Salt Score line when zero")
		}
	})

	t.Run("truncation suffix shown", func(t *testing.T) {
		page := &EDHRECResponse{
			Container: EDHRECContainer{
				JSONDict: EDHRECData{
					Card: EDHRECCardInfo{Name: "Test Commander", NumDecks: 100},
					CardLists: []EDHRECCardList{
						{Header: "Top Cards", CardViews: []EDHRECCardView{
							{Name: "Sol Ring", NumDecks: 90},
							{Name: "Arcane Signet", NumDecks: 80},
							{Name: "Command Tower", NumDecks: 70},
						}},
					},
				},
			},
		}
		got := FormatCommanderRecsForDisplay(page, EDHRECRecFilter{}, 1)
		if !strings.Contains(got, "and 2 more cards") {
			t.Errorf("FormatCommanderRecsForDisplay() missing truncation suffix, got:\n%s", got)
		}
	})
}

func TestRecFilterFromArgs(t *testing.T) {
	tests := []struct {
		name    string
		args    map[string]any
		want    EDHRECRecFilter
		wantErr bool
	}{
		{
			name: "no args",
			args: map[string]any{},
			want: EDHRECRecFilter{},
		},
		{
			name: "price tier is lowercased",
			args: map[string]any{"price_tier": "Budget"},
			want: EDHRECRecFilter{PriceTier: "budget"},
		},
		{
			name:    "invalid price tier names both valid values",
			args:    map[string]any{"price_tier": "middle"},
			wantErr: true,
		},
		{
			name:    "wrong-typed price tier is an error",
			args:    map[string]any{"price_tier": 42},
			wantErr: true,
		},
		{
			name: "theme is trimmed and lowercased",
			args: map[string]any{"theme": " Infect "},
			want: EDHRECRecFilter{Theme: "infect"},
		},
		{
			name:    "wrong-typed theme is an error",
			args:    map[string]any{"theme": true},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := recFilterFromArgs(tt.args)
			if (err != nil) != tt.wantErr {
				t.Fatalf("recFilterFromArgs() error = %v, wantErr %v", err, tt.wantErr)
			}
			if tt.wantErr {
				if tt.name == "invalid price tier names both valid values" {
					missingBudget := !strings.Contains(err.Error(), priceTierBudget)
					missingExpensive := !strings.Contains(err.Error(), priceTierExpensive)
					if missingBudget || missingExpensive {
						t.Errorf("error should name both valid tiers, got: %v", err)
					}
				}
				return
			}
			if got != tt.want {
				t.Errorf("recFilterFromArgs() = %+v, want %+v", got, tt.want)
			}
		})
	}
}
