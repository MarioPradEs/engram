package dashboard

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// ─── Block A: admin sub-nav contains Games + Departments links ────────────────

// TestAdminNavContainsGamesAndDepartments asserts that the adminNav component
// renders links for both Games and Departments for the given active values.
func TestAdminNavContainsGamesAndDepartments(t *testing.T) {
	mux := adminGamesMux([]string{"spark"}, nil, nil, nil)

	req := httptest.NewRequest(http.MethodGet, "/dashboard/admin/games?auth=ok", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%q", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, "/dashboard/admin/games") {
		t.Error("expected sub-nav to contain Games link (/dashboard/admin/games)")
	}
	if !strings.Contains(body, "/dashboard/admin/departments") {
		t.Error("expected sub-nav to contain Departments link (/dashboard/admin/departments)")
	}
}

// ─── Block A: compact games page ─────────────────────────────────────────────

// TestAdminGamesGET_CompactColorTable asserts that the refactored Games page
// renders a unified editable table (name input + color + Save + X per row).
// The old per-row /games/{name}/color action has been replaced by /games/save.
func TestAdminGamesGET_CompactColorTable(t *testing.T) {
	mux := adminGamesMux(
		[]string{"spark", "viva-clash"},
		map[string]string{"spark": "#E5C07B"},
		nil,
		nil,
	)

	req := httptest.NewRequest(http.MethodGet, "/dashboard/admin/games?auth=ok", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%q", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	// Editable table must include game names.
	if !strings.Contains(body, "spark") {
		t.Error("expected body to list game 'spark'")
	}
	if !strings.Contains(body, "viva-clash") {
		t.Error("expected body to list game 'viva-clash'")
	}
	// Color input must be present (type=color).
	if !strings.Contains(body, `type="color"`) {
		t.Error("expected editable table to include type=color inputs")
	}
	// Save action must point to the new /games/save route.
	if !strings.Contains(body, "/dashboard/admin/games/save") {
		t.Error("expected form action pointing to /dashboard/admin/games/save")
	}
}

// ─── Block A: redirect-after-save for game color ─────────────────────────────

// TestAdminGameColorPost_ValidHexReturns303 asserts that after a successful
// color save the handler redirects 303 to /dashboard/admin/games.
func TestAdminGameColorPost_ValidHexReturns303(t *testing.T) {
	mux := adminColorMux(func(_, _ string) error { return nil }, true)

	form := strings.NewReader("color=%23E5C07B")
	req := httptest.NewRequest(http.MethodPost, "/dashboard/admin/games/spark/color?auth=ok", form)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusSeeOther {
		t.Fatalf("expected 303 SeeOther after save, got %d body=%q", rec.Code, rec.Body.String())
	}
	loc := rec.Header().Get("Location")
	if !strings.HasPrefix(loc, "/dashboard/admin/games") {
		t.Errorf("expected Location to start with /dashboard/admin/games, got %q", loc)
	}
}

// ─── Departments editable table ───────────────────────────────────────────────

// adminDeptsMux builds a mux with the canonical departments editable table wired.
// depts is the canonical list from classrules. deptColors populates the ListColors
// dept colors. saveDept and deleteDept capture calls for assertion.
func adminDeptsMux(
	depts []string,
	deptEntries []DeptEntry,
	deptColors map[string]string,
	saveDept func(newDepts []DeptEntry, newDeptColors map[string]string) error,
	deleteDept func(newDepts []DeptEntry, newDeptColors map[string]string) error,
) *http.ServeMux {
	mux := http.NewServeMux()
	Mount(mux, MountConfig{
		RequireSession: func(r *http.Request) error {
			if r.URL.Query().Get("auth") == "ok" {
				return nil
			}
			return errUnauthorized
		},
		IsAdmin: func(_ *http.Request) bool { return true },
		ListDepartmentsCanonical: func() []string {
			return depts
		},
		ListDeptEntriesCanonical: func() []DeptEntry {
			return deptEntries
		},
		ListColors: func() (map[string]string, map[string]string) {
			return nil, deptColors
		},
		SaveDept:   saveDept,
		DeleteDept: deleteDept,
		// Keep WriteDeptColor wired so the legacy /departments/{name}/color route still works.
		WriteDeptColor: func(_, _ string) error { return nil },
	})
	return mux
}

// adminDepartmentsMux builds a mux with legacy dept color write wired (for legacy route tests).
// This builder is kept for the legacy POST /departments/{name}/color tests.
// It includes a default ListDepartmentsCanonical so the name-existence check in
// handleAdminDeptColorPost finds any department that also appears in the test fixture
// (the canonical list defaults to ["dev", "art", "qa"]).
func adminDepartmentsMux(provUsers []ProvisionedUser, deptColors map[string]string, writeColor func(name, color string) error) *http.ServeMux {
	return adminDepartmentsMuxWithDepts([]string{"dev", "art", "qa"}, provUsers, deptColors, writeColor)
}

// adminDepartmentsMuxWithDepts is like adminDepartmentsMux but accepts an explicit canonical dept list.
func adminDepartmentsMuxWithDepts(depts []string, provUsers []ProvisionedUser, deptColors map[string]string, writeColor func(name, color string) error) *http.ServeMux {
	mux := http.NewServeMux()
	Mount(mux, MountConfig{
		RequireSession: func(r *http.Request) error {
			if r.URL.Query().Get("auth") == "ok" {
				return nil
			}
			return errUnauthorized
		},
		IsAdmin: func(_ *http.Request) bool { return true },
		ListDepartmentsCanonical: func() []string {
			return depts
		},
		ListProvisionedUsers: func() []ProvisionedUser {
			return provUsers
		},
		ListColors: func() (map[string]string, map[string]string) {
			return nil, deptColors
		},
		WriteDeptColor: func(name, color string) error {
			if writeColor != nil {
				return writeColor(name, color)
			}
			return nil
		},
	})
	return mux
}

// TestAdminDepartmentsGET_RendersEditableTableFromCanonicalList asserts that
// GET /dashboard/admin/departments renders a row per canonical department with
// editable name input, color picker, Save and Delete (X), and an Add row.
// It sources from ListDepartmentsCanonical, NOT users.yaml.
func TestAdminDepartmentsGET_RendersEditableTableFromCanonicalList(t *testing.T) {
	depts := []string{"dev", "art"}
	entries := []DeptEntry{
		{Name: "dev", Aliases: []string{"engineering"}},
		{Name: "art"},
	}
	mux := adminDeptsMux(depts, entries, map[string]string{"dev": "#528BFF"}, nil, nil)

	req := httptest.NewRequest(http.MethodGet, "/dashboard/admin/departments?auth=ok", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%q", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()

	// Table must include canonical dept names.
	if !strings.Contains(body, "dev") {
		t.Error("expected body to list department 'dev'")
	}
	if !strings.Contains(body, "art") {
		t.Error("expected body to list department 'art'")
	}
	// Color input must be present (type=color).
	if !strings.Contains(body, `type="color"`) {
		t.Error("expected editable table to include type=color inputs")
	}
	// Color input must reflect current color.
	if !strings.Contains(body, "#528BFF") {
		t.Error("expected body to contain current dept color #528BFF")
	}
	// Save action must point to /departments/save (not /departments/{name}/color).
	if !strings.Contains(body, "/dashboard/admin/departments/save") {
		t.Error("expected form action pointing to /dashboard/admin/departments/save")
	}
	// Must render editable name inputs.
	if !strings.Contains(body, `type="text"`) {
		t.Error("expected editable table to contain text inputs for dept names")
	}
	// Must render Save buttons.
	if !strings.Contains(body, "Save") {
		t.Error("expected body to contain Save button")
	}
	// Must render Delete (X) buttons.
	if !strings.Contains(body, ">X<") {
		t.Error("expected body to contain delete X button")
	}
	// Must render the add-row.
	if !strings.Contains(body, "new department") {
		t.Error("expected body to contain add-row placeholder")
	}
}

// TestAdminDepartmentsGET_AdminOnly asserts that non-admin requests get 403.
func TestAdminDepartmentsGET_AdminOnly(t *testing.T) {
	mux := http.NewServeMux()
	Mount(mux, MountConfig{
		RequireSession: func(r *http.Request) error {
			if r.URL.Query().Get("auth") == "ok" {
				return nil
			}
			return errUnauthorized
		},
		IsAdmin: func(_ *http.Request) bool { return false },
	})

	req := httptest.NewRequest(http.MethodGet, "/dashboard/admin/departments?auth=ok", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403 for non-admin, got %d", rec.Code)
	}
}

// TestAdminDeptSavePost_AddDept asserts that POST /dashboard/admin/departments/save
// with original="" and a new name calls SaveDept with the extended list + color,
// then redirects 303 to /dashboard/admin/departments.
func TestAdminDeptSavePost_AddDept(t *testing.T) {
	var savedDepts []DeptEntry
	var savedColors map[string]string

	mux := adminDeptsMux(
		[]string{"dev"},
		[]DeptEntry{{Name: "dev", Aliases: []string{"engineering"}}},
		map[string]string{"dev": "#528BFF"},
		func(nd []DeptEntry, nc map[string]string) error {
			savedDepts = nd
			savedColors = nc
			return nil
		},
		nil,
	)

	form := strings.NewReader("original=&name=art&color=%23C678DD")
	req := httptest.NewRequest(http.MethodPost, "/dashboard/admin/departments/save?auth=ok", form)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusSeeOther {
		t.Fatalf("expected 303, got %d body=%q", rec.Code, rec.Body.String())
	}
	loc := rec.Header().Get("Location")
	if !strings.HasPrefix(loc, "/dashboard/admin/departments") {
		t.Errorf("expected redirect to /dashboard/admin/departments, got %q", loc)
	}
	if len(savedDepts) != 2 {
		t.Fatalf("expected 2 depts after add, got %v", savedDepts)
	}
	found := false
	for _, d := range savedDepts {
		if d.Name == "art" {
			found = true
		}
	}
	if !found {
		t.Error("art not found in saved depts list")
	}
	if savedColors["art"] != "#C678DD" {
		t.Errorf("art color = %q, want #C678DD", savedColors["art"])
	}
}

// TestAdminDeptSavePost_RenameMigratesColorAndPreservesAliases asserts that
// POST /dashboard/admin/departments/save with original!=name renames the dept,
// migrates the color, preserves aliases, and removes the old color key.
func TestAdminDeptSavePost_RenameMigratesColorAndPreservesAliases(t *testing.T) {
	var savedDepts []DeptEntry
	var savedColors map[string]string

	mux := adminDeptsMux(
		[]string{"dev", "art"},
		[]DeptEntry{
			{Name: "dev", Aliases: []string{"engineering", "eng"}},
			{Name: "art"},
		},
		map[string]string{"dev": "#528BFF", "art": "#C678DD"},
		func(nd []DeptEntry, nc map[string]string) error {
			savedDepts = nd
			savedColors = nc
			return nil
		},
		nil,
	)

	// Rename "dev" → "engineering-team"; color changes too.
	form := strings.NewReader("original=dev&name=engineering-team&color=%23528BFF")
	req := httptest.NewRequest(http.MethodPost, "/dashboard/admin/departments/save?auth=ok", form)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusSeeOther {
		t.Fatalf("expected 303, got %d body=%q", rec.Code, rec.Body.String())
	}
	if len(savedDepts) != 2 {
		t.Fatalf("expected 2 depts after rename, got %v", savedDepts)
	}
	for _, d := range savedDepts {
		if d.Name == "dev" {
			t.Error("dev should not appear in saved depts list after rename")
		}
		if d.Name == "engineering-team" {
			// Aliases must be preserved.
			if len(d.Aliases) != 2 || d.Aliases[0] != "engineering" || d.Aliases[1] != "eng" {
				t.Errorf("aliases not preserved after rename: got %v", d.Aliases)
			}
		}
	}
	if _, exists := savedColors["dev"]; exists {
		t.Error("dev color key should not exist after rename")
	}
	if savedColors["engineering-team"] != "#528BFF" {
		t.Errorf("engineering-team color = %q, want #528BFF", savedColors["engineering-team"])
	}
	// art's color must be preserved.
	if savedColors["art"] != "#C678DD" {
		t.Errorf("art color = %q after rename of another dept, want #C678DD", savedColors["art"])
	}
}

// TestAdminDeptSavePost_ColorOnlyUpdate asserts that POST /dashboard/admin/departments/save
// with original==name only updates the color, leaving the dept list unchanged.
func TestAdminDeptSavePost_ColorOnlyUpdate(t *testing.T) {
	var savedDepts []DeptEntry
	var savedColors map[string]string

	mux := adminDeptsMux(
		[]string{"dev"},
		[]DeptEntry{{Name: "dev", Aliases: []string{"engineering"}}},
		map[string]string{"dev": "#528BFF"},
		func(nd []DeptEntry, nc map[string]string) error {
			savedDepts = nd
			savedColors = nc
			return nil
		},
		nil,
	)

	form := strings.NewReader("original=dev&name=dev&color=%23FFFFFF")
	req := httptest.NewRequest(http.MethodPost, "/dashboard/admin/departments/save?auth=ok", form)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusSeeOther {
		t.Fatalf("expected 303, got %d body=%q", rec.Code, rec.Body.String())
	}
	if len(savedDepts) != 1 || savedDepts[0].Name != "dev" {
		t.Errorf("expected depts=[dev], got %v", savedDepts)
	}
	if savedColors["dev"] != "#FFFFFF" {
		t.Errorf("dev color = %q after color-only update, want #FFFFFF", savedColors["dev"])
	}
}

// TestAdminDeptSavePost_DuplicateNameRejected asserts that adding a dept with an
// existing name redirects with an error flash and does NOT call SaveDept.
func TestAdminDeptSavePost_DuplicateNameRejected(t *testing.T) {
	saveCalled := false

	mux := adminDeptsMux(
		[]string{"dev", "art"},
		[]DeptEntry{{Name: "dev"}, {Name: "art"}},
		nil,
		func(nd []DeptEntry, nc map[string]string) error {
			saveCalled = true
			return nil
		},
		nil,
	)

	// Try to add "dev" which already exists.
	form := strings.NewReader("original=&name=dev&color=%23000000")
	req := httptest.NewRequest(http.MethodPost, "/dashboard/admin/departments/save?auth=ok", form)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusSeeOther {
		t.Fatalf("expected 303 redirect on error, got %d", rec.Code)
	}
	loc := rec.Header().Get("Location")
	if !strings.Contains(loc, "flashErr=1") {
		t.Errorf("expected error flash in redirect, got location %q", loc)
	}
	if saveCalled {
		t.Error("SaveDept must NOT be called when name is duplicate")
	}
}

// TestAdminDeptSavePost_EmptyNameRejected asserts that an empty name redirects
// with an error flash and does NOT call SaveDept.
func TestAdminDeptSavePost_EmptyNameRejected(t *testing.T) {
	saveCalled := false

	mux := adminDeptsMux(
		[]string{"dev"},
		[]DeptEntry{{Name: "dev"}},
		nil,
		func(nd []DeptEntry, nc map[string]string) error {
			saveCalled = true
			return nil
		},
		nil,
	)

	form := strings.NewReader("original=&name=&color=%23000000")
	req := httptest.NewRequest(http.MethodPost, "/dashboard/admin/departments/save?auth=ok", form)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusSeeOther {
		t.Fatalf("expected 303 on error, got %d", rec.Code)
	}
	loc := rec.Header().Get("Location")
	if !strings.Contains(loc, "flashErr=1") {
		t.Errorf("expected error flash, got %q", loc)
	}
	if saveCalled {
		t.Error("SaveDept must NOT be called when name is empty")
	}
}

// TestAdminDeptSavePost_NonAdminReturns403 asserts that a non-admin request
// to POST /dashboard/admin/departments/save returns 403.
func TestAdminDeptSavePost_NonAdminReturns403(t *testing.T) {
	mux := http.NewServeMux()
	Mount(mux, MountConfig{
		RequireSession: func(r *http.Request) error {
			if r.URL.Query().Get("auth") == "ok" {
				return nil
			}
			return errUnauthorized
		},
		IsAdmin: func(_ *http.Request) bool { return false },
	})

	form := strings.NewReader("original=&name=dev&color=%23000000")
	req := httptest.NewRequest(http.MethodPost, "/dashboard/admin/departments/save?auth=ok", form)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d", rec.Code)
	}
}

// TestAdminDeptDeletePost_RemovesDeptAndColor asserts that POST
// /dashboard/admin/departments/delete calls DeleteDept with the dept removed
// from the list and its color deleted.
func TestAdminDeptDeletePost_RemovesDeptAndColor(t *testing.T) {
	var savedDepts []DeptEntry
	var savedColors map[string]string

	mux := adminDeptsMux(
		[]string{"dev", "art"},
		[]DeptEntry{
			{Name: "dev", Aliases: []string{"engineering"}},
			{Name: "art"},
		},
		map[string]string{"dev": "#528BFF", "art": "#C678DD"},
		nil,
		func(nd []DeptEntry, nc map[string]string) error {
			savedDepts = nd
			savedColors = nc
			return nil
		},
	)

	form := strings.NewReader("name=art")
	req := httptest.NewRequest(http.MethodPost, "/dashboard/admin/departments/delete?auth=ok", form)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusSeeOther {
		t.Fatalf("expected 303, got %d body=%q", rec.Code, rec.Body.String())
	}
	if len(savedDepts) != 1 || savedDepts[0].Name != "dev" {
		t.Errorf("expected [dev] after delete, got %v", savedDepts)
	}
	if _, exists := savedColors["art"]; exists {
		t.Error("art color should be removed after delete")
	}
	if savedColors["dev"] != "#528BFF" {
		t.Errorf("dev color should be preserved, got %q", savedColors["dev"])
	}
}

// TestAdminDeptDeletePost_NonAdminReturns403 asserts that a non-admin request
// to POST /dashboard/admin/departments/delete returns 403.
func TestAdminDeptDeletePost_NonAdminReturns403(t *testing.T) {
	mux := http.NewServeMux()
	Mount(mux, MountConfig{
		RequireSession: func(r *http.Request) error {
			if r.URL.Query().Get("auth") == "ok" {
				return nil
			}
			return errUnauthorized
		},
		IsAdmin: func(_ *http.Request) bool { return false },
	})

	form := strings.NewReader("name=art")
	req := httptest.NewRequest(http.MethodPost, "/dashboard/admin/departments/delete?auth=ok", form)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d", rec.Code)
	}
}

// ─── Legacy: redirect-after-save for dept color (POST /departments/{name}/color) ─

// TestAdminDeptColorPost_ValidHexReturns303 asserts that the legacy POST
// /dashboard/admin/departments/{name}/color with a valid hex redirects 303.
// This route is kept for backwards compatibility.
func TestAdminDeptColorPost_ValidHexReturns303(t *testing.T) {
	written := map[string]string{}
	mux := adminDepartmentsMux(nil, nil, func(name, color string) error {
		written[name] = color
		return nil
	})

	form := strings.NewReader("color=%23528BFF")
	req := httptest.NewRequest(http.MethodPost, "/dashboard/admin/departments/dev/color?auth=ok", form)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusSeeOther {
		t.Fatalf("expected 303 SeeOther after dept save, got %d body=%q", rec.Code, rec.Body.String())
	}
	loc := rec.Header().Get("Location")
	if !strings.HasPrefix(loc, "/dashboard/admin/departments") {
		t.Errorf("expected Location to start with /dashboard/admin/departments, got %q", loc)
	}
	if written["dev"] != "#528BFF" {
		t.Errorf("WriteDeptColor called with name=%q color=%q; want dev #528BFF", "dev", written["dev"])
	}
}

// TestAdminDeptColorPost_NonAdminReturns403 asserts that non-admin requests to
// the dept color endpoint get 403.
func TestAdminDeptColorPost_NonAdminReturns403(t *testing.T) {
	mux := http.NewServeMux()
	Mount(mux, MountConfig{
		RequireSession: func(r *http.Request) error {
			if r.URL.Query().Get("auth") == "ok" {
				return nil
			}
			return errUnauthorized
		},
		IsAdmin: func(_ *http.Request) bool { return false },
	})

	form := strings.NewReader("color=%23528BFF")
	req := httptest.NewRequest(http.MethodPost, "/dashboard/admin/departments/dev/color?auth=ok", form)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403 for non-admin dept color save, got %d", rec.Code)
	}
}

// TestAdminDeptColorPost_InvalidHexReturns400 asserts that an invalid color
// returns 400 without calling WriteDeptColor.
func TestAdminDeptColorPost_InvalidHexReturns400(t *testing.T) {
	called := false
	mux := adminDepartmentsMux(nil, nil, func(_, _ string) error {
		called = true
		return nil
	})

	form := strings.NewReader("color=red")
	req := httptest.NewRequest(http.MethodPost, "/dashboard/admin/departments/dev/color?auth=ok", form)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for invalid color, got %d", rec.Code)
	}
	if called {
		t.Error("WriteDeptColor must NOT be called when color is invalid")
	}
}

// TestAdminDeptColorPost_UnknownNameRejected asserts that POSTing a color for a
// department that does not exist in the canonical list redirects with an error flash
// and does NOT call WriteDeptColor.
func TestAdminDeptColorPost_UnknownNameRejected(t *testing.T) {
	writeCalled := false
	mux := adminDepartmentsMuxWithDepts([]string{"dev", "art"}, nil, nil, func(_, _ string) error {
		writeCalled = true
		return nil
	})

	form := strings.NewReader("color=%23528BFF")
	req := httptest.NewRequest(http.MethodPost, "/dashboard/admin/departments/ghost/color?auth=ok", form)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusSeeOther {
		t.Fatalf("expected 303 redirect on unknown dept name, got %d", rec.Code)
	}
	loc := rec.Header().Get("Location")
	if !strings.Contains(loc, "flashErr=1") {
		t.Errorf("expected error flash for unknown dept, got location %q", loc)
	}
	if writeCalled {
		t.Error("WriteDeptColor must NOT be called for an unknown department name")
	}
}

// TestAdminDeptSavePost_InvalidNameRejected asserts that a department name with
// disallowed characters redirects with an error flash and does NOT call SaveDept.
func TestAdminDeptSavePost_InvalidNameRejected(t *testing.T) {
	saveCalled := false
	mux := adminDeptsMux(
		[]string{"dev"},
		[]DeptEntry{{Name: "dev"}},
		nil,
		func(nd []DeptEntry, nc map[string]string) error {
			saveCalled = true
			return nil
		},
		nil,
	)

	// "<script>" is not in the allowlist.
	form := strings.NewReader("original=&name=%3Cscript%3E&color=")
	req := httptest.NewRequest(http.MethodPost, "/dashboard/admin/departments/save?auth=ok", form)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusSeeOther {
		t.Fatalf("expected 303 redirect on invalid name, got %d", rec.Code)
	}
	loc := rec.Header().Get("Location")
	if !strings.Contains(loc, "flashErr=1") {
		t.Errorf("expected error flash for invalid name, got location %q", loc)
	}
	if saveCalled {
		t.Error("SaveDept must NOT be called when name fails allowlist check")
	}
}

// TestAdminDeptSavePost_CaseSensitiveRename asserts that renaming "Dev" → "dev"
// (case-only change) is treated as color-only (not a ghost-add).
func TestAdminDeptSavePost_CaseSensitiveRename(t *testing.T) {
	var savedDepts []DeptEntry
	mux := adminDeptsMux(
		[]string{"Dev"},
		[]DeptEntry{{Name: "Dev"}},
		map[string]string{"Dev": "#528BFF"},
		func(nd []DeptEntry, nc map[string]string) error {
			savedDepts = nd
			return nil
		},
		nil,
	)

	// original=Dev&name=dev — same name, different case.
	form := strings.NewReader("original=Dev&name=dev&color=%23528BFF")
	req := httptest.NewRequest(http.MethodPost, "/dashboard/admin/departments/save?auth=ok", form)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusSeeOther {
		t.Fatalf("expected 303, got %d body=%q", rec.Code, rec.Body.String())
	}
	// Must not ghost-add "dev" — list length must stay 1.
	if len(savedDepts) != 1 {
		t.Errorf("case-only rename must not add a second dept entry; got %v", savedDepts)
	}
}
