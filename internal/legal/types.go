package legal

// Document types stored in legal_documents / legal_acceptances.
// legal_pending_resume.kind values are defined in bots (e.g. driver_relive = re-share live location after legal interrupt while online/live).
const (
	DocDriverTerms   = "driver_terms"
	DocUserTerms     = "user_terms"
	DocPrivacyPolicy = "privacy_policy"
	ErrCodeRequired  = "LEGAL_ACCEPTANCE_REQUIRED"
	RiderDocTypes    = 2
	DriverDocTypes   = 3
)

// SQLDriverDispatchLegalOK is appended to driver dispatch queries: requires all three active acceptances.
// Expects outer alias `d` for drivers with d.user_id.
const SQLDriverDispatchLegalOK = `3 = (
  SELECT COUNT(*) FROM legal_acceptances la
  INNER JOIN legal_documents ld ON ld.document_type = la.document_type AND ld.version = la.version AND ld.is_active = 1
  WHERE la.user_id = d.user_id
  AND la.document_type IN ('driver_terms','user_terms','privacy_policy')
)`
