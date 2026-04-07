package handlers

import (
	"context"
	"database/sql"
	"net/http"
	"sort"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"taxi-mvp/internal/legal"
)

// RegisterAdminLegalRoutes mounts GET/HEAD .../admin/legal/* on the API.
// Registers under /admin, /api/admin, /api/v1/admin, and /v1/admin so dashboards that probe different API prefixes all work.
func RegisterAdminLegalRoutes(r *gin.Engine, db *sql.DB) {
	if r == nil || db == nil {
		return
	}
	for _, base := range []string{"/admin", "/api/admin", "/api/v1/admin", "/v1/admin"} {
		g := r.Group(base)
		registerAdminLegalRoutes(g, db)
	}
}

// registerAdminLegalRoutes mounts /admin/legal/* under an existing /admin RouterGroup.
func registerAdminLegalRoutes(g *gin.RouterGroup, db *sql.DB) {
	if g == nil || db == nil {
		return
	}
	h := &adminLegalHTTP{db: db}
	lg := g.Group("/legal")
	{
		lg.GET("", h.monitoring)
		lg.HEAD("", h.monitoringHead)
		lg.GET("/monitoring", h.monitoring)
		lg.HEAD("/monitoring", h.monitoringHead)
		lg.GET("/status", h.monitoring)
		lg.HEAD("/status", h.monitoringHead)
		lg.GET("/summary", h.monitoring)
		lg.GET("/health", h.monitoring)
		lg.HEAD("/health", h.monitoringHead)
		lg.GET("/stats", h.legalStats)
		lg.HEAD("/stats", h.legalStatsHead)
		lg.GET("/issues", h.issues)
		lg.HEAD("/issues", h.issuesHead)
		lg.GET("/problems", h.issues)
		lg.HEAD("/problems", h.issuesHead)
		lg.GET("/documents", h.documents)
		lg.HEAD("/documents", h.documentsHead)
		lg.GET("/acceptances", h.allAcceptances)
		lg.HEAD("/acceptances", h.allAcceptancesHead)
		lg.GET("/missing", h.missingLegal)
		lg.HEAD("/missing", h.missingLegalHead)
		lg.GET("/users/:user_id/acceptances", h.userAcceptances)
		lg.HEAD("/users/:user_id/acceptances", h.userAcceptancesHead)
	}
}

func (h *adminLegalHTTP) monitoringHead(c *gin.Context) {
	c.Status(http.StatusOK)
}

func (h *adminLegalHTTP) issuesHead(c *gin.Context) {
	c.Status(http.StatusOK)
}

func (h *adminLegalHTTP) documentsHead(c *gin.Context) {
	c.Status(http.StatusOK)
}

func (h *adminLegalHTTP) userAcceptancesHead(c *gin.Context) {
	c.Status(http.StatusOK)
}

func (h *adminLegalHTTP) allAcceptancesHead(c *gin.Context) {
	c.Status(http.StatusOK)
}

func (h *adminLegalHTTP) missingLegalHead(c *gin.Context) {
	c.Status(http.StatusOK)
}

func (h *adminLegalHTTP) legalStatsHead(c *gin.Context) {
	c.Status(http.StatusOK)
}

type adminLegalHTTP struct {
	db *sql.DB
}

func dashboardLegalDocumentCode(documentType string) string {
	switch strings.TrimSpace(documentType) {
	case legal.DocPrivacyPolicyDriver, legal.DocPrivacyPolicyUser, legal.DocPrivacyPolicy:
		// Dashboard expects legacy privacy_policy for both actors.
		return legal.DocPrivacyPolicy
	default:
		return strings.TrimSpace(documentType)
	}
}

func dashboardActorType(role string) string {
	switch strings.TrimSpace(strings.ToLower(role)) {
	case "driver":
		return "driver"
	default:
		// Dashboards tend to use "user" rather than "rider".
		return "user"
	}
}

func legalAcceptanceJSON(userID int64, documentType string, version int, acceptedAt string, matchesActive bool, role, userName, clientIP, userAgent string) gin.H {
	ip := strings.TrimSpace(clientIP)
	ua := strings.TrimSpace(userAgent)
	lab := versionLabel(version)
	code := dashboardLegalDocumentCode(documentType)
	actorType := dashboardActorType(role)
	return gin.H{
		"user_id":                userID,
		"actor_id":               userID,
		"actor_type":             actorType,
		"document_type":          documentType,
		"document_code":          code,
		"version":                version,
		"document_version":       version,
		"version_label":          lab,
		"version_string":         lab,
		"accepted_at":            acceptedAt,
		"matches_active_version": matchesActive,
		"ip_address":             ip,
		"client_ip":              ip,
		"user_agent":             ua,
		"role":                   role,
		"user_name":              userName,
	}
}

// buildLegalMonitoringData loads active docs and compliance counts using legal_acceptances vs active legal_documents only.
func (h *adminLegalHTTP) buildLegalMonitoringData(ctx context.Context) (docList []gin.H, counts gin.H, err error) {
	svc := legal.NewService(h.db)
	types := []string{legal.DocDriverTerms, legal.DocUserTerms, legal.DocPrivacyPolicyUser, legal.DocPrivacyPolicyDriver}
	docs, err := svc.ActiveDocuments(ctx, types)
	if err != nil {
		return nil, nil, err
	}
	docList = make([]gin.H, 0, len(types))
	for _, t := range types {
		if d, ok := docs[t]; ok {
			prev := d.Content
			if len(prev) > 240 {
				prev = prev[:240] + "…"
			}
			lab := versionLabel(d.Version)
			docList = append(docList, gin.H{
				"document_type":          t,
				"document_code":          dashboardLegalDocumentCode(t),
				"version":                d.Version,
				"document_version":       d.Version,
				"version_label":          lab,
				"version_string":         lab,
				"content_preview":        strings.TrimSpace(prev),
				"matches_active_version": true,
			})
		}
	}

	active, err := loadActiveDocumentVersions(ctx, h.db)
	if err != nil {
		return nil, nil, err
	}
	idx, err := loadAcceptanceIndexForDriversAndRiders(ctx, h.db)
	if err != nil {
		return nil, nil, err
	}

	var driversTotal, driversOK, ridersTotal, ridersOK int64
	_ = h.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM drivers`).Scan(&driversTotal)
	dRows, err := h.db.QueryContext(ctx, `SELECT user_id FROM drivers`)
	if err != nil {
		return nil, nil, err
	}
	for dRows.Next() {
		var uid int64
		if err := dRows.Scan(&uid); err != nil {
			_ = dRows.Close()
			return nil, nil, err
		}
		if len(missingDocsForUser(uid, "driver", active, idx)) == 0 {
			driversOK++
		}
	}
	_ = dRows.Close()

	_ = h.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM users WHERE role = 'rider'`).Scan(&ridersTotal)
	rRows, err := h.db.QueryContext(ctx, `SELECT id FROM users WHERE role = 'rider'`)
	if err != nil {
		return nil, nil, err
	}
	for rRows.Next() {
		var uid int64
		if err := rRows.Scan(&uid); err != nil {
			_ = rRows.Close()
			return nil, nil, err
		}
		if len(missingDocsForUser(uid, "rider", active, idx)) == 0 {
			ridersOK++
		}
	}
	_ = rRows.Close()

	var totalAcc int64
	_ = h.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM legal_acceptances`).Scan(&totalAcc)

	counts = gin.H{
		"drivers_total":           driversTotal,
		"drivers_fully_compliant": driversOK,
		"riders_total":            ridersTotal,
		"riders_fully_compliant":  ridersOK,
		"drivers_missing_legal":   driversTotal - driversOK,
		"riders_missing_legal":    ridersTotal - ridersOK,
		"total_acceptances":       totalAcc,
	}
	return docList, counts, nil
}

func (h *adminLegalHTTP) monitoring(c *gin.Context) {
	ctx := c.Request.Context()
	docList, counts, err := h.buildLegalMonitoringData(ctx)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"ok": false, "error": "legal tables or query failed", "detail": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"ok":                    true,
		"enabled":               true,
		"service":               "taxi-mvp",
		"active_documents":      docList,
		"active_document_count": len(docList),
		"counts":                counts,
	})
}

// legalStats is the dashboard probe path /admin/legal/stats (same data as monitoring, plus nested "stats").
func (h *adminLegalHTTP) legalStats(c *gin.Context) {
	ctx := c.Request.Context()
	docList, counts, err := h.buildLegalMonitoringData(ctx)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"ok": false, "error": "legal tables or query failed", "detail": err.Error()})
		return
	}
	n := len(docList)
	stats := gin.H{"active_document_count": n}
	for k, v := range counts {
		stats[k] = v
	}
	// CamelCase aliases for dashboards that expect JS-style keys (avoids "-" / undefined reads).
	stats["driversTotal"] = counts["drivers_total"]
	stats["driversFullyCompliant"] = counts["drivers_fully_compliant"]
	stats["ridersTotal"] = counts["riders_total"]
	stats["ridersFullyCompliant"] = counts["riders_fully_compliant"]
	stats["driversMissingLegal"] = counts["drivers_missing_legal"]
	stats["ridersMissingLegal"] = counts["riders_missing_legal"]
	stats["totalAcceptances"] = counts["total_acceptances"]
	stats["activeDocumentCount"] = n

	c.JSON(http.StatusOK, gin.H{
		"ok":                    true,
		"enabled":               true,
		"service":               "taxi-mvp",
		"stats":                 stats,
		"active_documents":      docList,
		"active_document_count": n,
		"counts":                counts,
	})
}

func (h *adminLegalHTTP) issues(c *gin.Context) {
	ctx := c.Request.Context()
	list, err := h.computeNonCompliant(ctx, "all")
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"ok": false, "error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true, "issues": list, "count": len(list)})
}

// computeNonCompliant lists drivers/riders missing at least one required acceptance at the currently active version.
func (h *adminLegalHTTP) computeNonCompliant(ctx context.Context, actorFilter string) ([]gin.H, error) {
	active, err := loadActiveDocumentVersions(ctx, h.db)
	if err != nil {
		return nil, err
	}
	idx, err := loadAcceptanceIndexForDriversAndRiders(ctx, h.db)
	if err != nil {
		return nil, err
	}
	type row struct {
		uid  int64
		role string
		miss []string
	}
	var buf []row
	if actorFilter == "all" || actorFilter == "driver" {
		dRows, err := h.db.QueryContext(ctx, `SELECT user_id FROM drivers`)
		if err != nil {
			return nil, err
		}
		for dRows.Next() {
			var uid int64
			if err := dRows.Scan(&uid); err != nil {
				_ = dRows.Close()
				return nil, err
			}
			miss := missingDocsForUser(uid, "driver", active, idx)
			if len(miss) > 0 {
				buf = append(buf, row{uid, "driver", miss})
			}
		}
		_ = dRows.Close()
	}
	if actorFilter == "all" || actorFilter == "rider" {
		rRows, err := h.db.QueryContext(ctx, `SELECT id FROM users WHERE role = 'rider'`)
		if err != nil {
			return nil, err
		}
		for rRows.Next() {
			var uid int64
			if err := rRows.Scan(&uid); err != nil {
				_ = rRows.Close()
				return nil, err
			}
			miss := missingDocsForUser(uid, "rider", active, idx)
			if len(miss) > 0 {
				buf = append(buf, row{uid, "rider", miss})
			}
		}
		_ = rRows.Close()
	}
	sort.Slice(buf, func(i, j int) bool { return buf[i].uid > buf[j].uid })
	out := make([]gin.H, 0, len(buf))
	for _, r := range buf {
		mapped := make([]string, 0, len(r.miss))
		for _, dt := range r.miss {
			mapped = append(mapped, dashboardLegalDocumentCode(dt))
		}
		out = append(out, gin.H{
			"user_id":           r.uid,
			"actor_id":          r.uid,
			"actor_type":        dashboardActorType(r.role),
			"role":              r.role,
			// Prefer dashboard-friendly codes (privacy_policy legacy) while still exposing raw types for debugging.
			"missing_documents":      mapped,
			"missing_document_types": r.miss,
		})
	}
	return out, nil
}

// missingLegal lists users missing required acceptances for active document versions.
// Query actor_type: driver | rider | all (default all). Matches dashboard probes, e.g. ?actor_type=driver
func (h *adminLegalHTTP) missingLegal(c *gin.Context) {
	ctx := c.Request.Context()
	at := strings.TrimSpace(strings.ToLower(c.Query("actor_type")))
	switch at {
	case "", "all":
		at = "all"
	case "driver", "drivers":
		at = "driver"
	case "rider", "riders":
		at = "rider"
	default:
		c.JSON(http.StatusBadRequest, gin.H{
			"ok":    false,
			"error": "invalid actor_type; use driver, rider, or all",
		})
		return
	}

	list, err := h.computeNonCompliant(ctx, at)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"ok": false, "error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"ok":         true,
		"actor_type": at,
		"missing":    list,
		"count":      len(list),
	})
}

func (h *adminLegalHTTP) documents(c *gin.Context) {
	ctx := c.Request.Context()
	rows, err := h.db.QueryContext(ctx, `
		SELECT document_type, version, is_active,
		       CASE WHEN LENGTH(content) > 400 THEN SUBSTR(content, 1, 400) || '…' ELSE content END
		FROM legal_documents
		ORDER BY document_type, version DESC`)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"ok": false, "error": err.Error()})
		return
	}
	defer rows.Close()
	var out []gin.H
	for rows.Next() {
		var dt string
		var ver, active int
		var body string
		if err := rows.Scan(&dt, &ver, &active, &body); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"ok": false, "error": err.Error()})
			return
		}
		lab := versionLabel(ver)
		out = append(out, gin.H{
			"document_type":    dt,
			"document_code":    dashboardLegalDocumentCode(dt),
			"version":          ver,
			"document_version": ver,
			"version_label":    lab,
			"version_string":   lab,
			"is_active":        active == 1,
			"content":          body,
		})
	}
	if err := rows.Err(); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"ok": false, "error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true, "documents": out})
}

// allAcceptances returns all rows from legal_acceptances (newest first) for admin dashboards.
// Query: limit (default 2000, max 10000), offset (default 0), user_id (optional filter — same as /users/:id/acceptances for modal clients).
func (h *adminLegalHTTP) allAcceptances(c *gin.Context) {
	ctx := c.Request.Context()
	limit := 2000
	if s := c.Query("limit"); s != "" {
		if n, err := strconv.Atoi(s); err == nil && n > 0 {
			limit = n
		}
	}
	if limit > 10000 {
		limit = 10000
	}
	offset := 0
	if s := c.Query("offset"); s != "" {
		if n, err := strconv.Atoi(s); err == nil && n >= 0 {
			offset = n
		}
	}
	var filterUID int64
	if s := c.Query("user_id"); s != "" {
		if n, err := strconv.ParseInt(s, 10, 64); err == nil && n > 0 {
			filterUID = n
		}
	}

	q := `
		SELECT la.user_id,
		       la.document_type,
		       la.version,
		       la.accepted_at,
		       EXISTS(SELECT 1 FROM legal_documents ld
		              WHERE ld.document_type = la.document_type AND ld.version = la.version AND ld.is_active = 1) AS matches_active,
		       COALESCE(u.role, '') AS role,
		       COALESCE(u.name, '') AS user_name,
		       COALESCE(la.client_ip, '') AS client_ip,
		       COALESCE(la.user_agent, '') AS user_agent
		FROM legal_acceptances la
		LEFT JOIN users u ON u.id = la.user_id`
	args := []interface{}{}
	if filterUID > 0 {
		q += ` WHERE la.user_id = ?1`
		args = append(args, filterUID)
		q += ` ORDER BY la.accepted_at DESC, la.user_id DESC LIMIT ?2 OFFSET ?3`
		args = append(args, limit, offset)
	} else {
		q += ` ORDER BY la.accepted_at DESC, la.user_id DESC LIMIT ?1 OFFSET ?2`
		args = append(args, limit, offset)
	}
	rows, err := h.db.QueryContext(ctx, q, args...)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"ok": false, "error": err.Error()})
		return
	}
	defer rows.Close()
	var list []gin.H
	for rows.Next() {
		var uid int64
		var dt, at, role, name, cip, uag string
		var ver, match int
		if err := rows.Scan(&uid, &dt, &ver, &at, &match, &role, &name, &cip, &uag); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"ok": false, "error": err.Error()})
			return
		}
		list = append(list, legalAcceptanceJSON(uid, dt, ver, at, match != 0, role, name, cip, uag))
	}
	if err := rows.Err(); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"ok": false, "error": err.Error()})
		return
	}
	resp := gin.H{"ok": true, "acceptances": list, "records": list, "count": len(list), "limit": limit, "offset": offset}
	if filterUID > 0 {
		resp["user_id"] = filterUID
		resp["actor_id"] = filterUID
	}
	c.JSON(http.StatusOK, resp)
}

func (h *adminLegalHTTP) userAcceptances(c *gin.Context) {
	ctx := c.Request.Context()
	idStr := c.Param("user_id")
	uid, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil || uid <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"ok": false, "error": "invalid user_id"})
		return
	}
	var role, name string
	_ = h.db.QueryRowContext(ctx, `SELECT COALESCE(role,''), COALESCE(name,'') FROM users WHERE id = ?1`, uid).Scan(&role, &name)
	rows, err := h.db.QueryContext(ctx, `
		SELECT la.document_type, la.version, la.accepted_at,
		       EXISTS(SELECT 1 FROM legal_documents ld
		              WHERE ld.document_type = la.document_type AND ld.version = la.version AND ld.is_active = 1) AS matches_active,
		       COALESCE(la.client_ip, '') AS client_ip,
		       COALESCE(la.user_agent, '') AS user_agent
		FROM legal_acceptances la
		WHERE la.user_id = ?1
		ORDER BY la.accepted_at DESC`, uid)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"ok": false, "error": err.Error()})
		return
	}
	defer rows.Close()
	var out []gin.H
	for rows.Next() {
		var dt, at, cip, uag string
		var ver, matchActive int
		if err := rows.Scan(&dt, &ver, &at, &matchActive, &cip, &uag); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"ok": false, "error": err.Error()})
			return
		}
		out = append(out, legalAcceptanceJSON(uid, dt, ver, at, matchActive != 0, role, name, cip, uag))
	}
	if err := rows.Err(); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"ok": false, "error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"ok":            true,
		"user_id":       uid,
		"actor_id":      uid,
		"acceptances":   out,
		"records":       out,
		"history":       out,
		"count":         len(out),
	})
}
