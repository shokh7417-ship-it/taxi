package repositories

import (
	"context"
	"database/sql"
	"fmt"

	"taxi-mvp/internal/models"
)

// AdminDriverRepository defines read/write operations for admin driver balance views.
type AdminDriverRepository interface {
	ListDriversWithBalance(ctx context.Context) ([]models.Driver, error)
	ListRidersForAdmin(ctx context.Context) ([]models.AdminRiderDTO, error)
	GetDriverByID(ctx context.Context, id int64) (*models.Driver, error)
	SetDriverBalance(ctx context.Context, id int64, newBalance int64) error
	UpdateVerificationStatus(ctx context.Context, driverUserID int64, status string) error
	GetDriverTelegramID(ctx context.Context, driverUserID int64) (int64, error)
}

type adminDriverRepo struct {
	db *sql.DB
}

// NewAdminDriverRepository returns an AdminDriverRepository backed by *sql.DB.
func NewAdminDriverRepository(db *sql.DB) AdminDriverRepository {
	return &adminDriverRepo{db: db}
}

const legalJoinActive = `INNER JOIN legal_documents ld ON ld.document_type = la.document_type AND ld.version = la.version AND ld.is_active = 1`

// ListDriversWithBalance returns drivers ordered by user_id DESC with balance, legal flags (active document versions), and verification_status.
func (r *adminDriverRepo) ListDriversWithBalance(ctx context.Context) ([]models.Driver, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT u.id AS id,
		       COALESCE(u.name, '') AS name,
		       COALESCE(d.phone, '') AS phone,
		       COALESCE(d.car_type, '') AS car_model,
		       COALESCE(d.plate, '') AS plate_number,
		       d.promo_balance,
		       d.cash_balance,
		       d.balance,
		       d.total_paid,
		       COALESCE(d.verification_status, '') AS verification_status,
		       CASE WHEN EXISTS(SELECT 1 FROM legal_acceptances la `+legalJoinActive+`
		              WHERE la.user_id = d.user_id AND la.document_type = 'driver_terms') THEN 1 ELSE 0 END AS has_driver_terms,
		       CASE WHEN EXISTS(SELECT 1 FROM legal_acceptances la `+legalJoinActive+`
		              WHERE la.user_id = d.user_id AND la.document_type = 'user_terms') THEN 1 ELSE 0 END AS has_user_terms,
		       CASE WHEN EXISTS(SELECT 1 FROM legal_acceptances la `+legalJoinActive+`
		              WHERE la.user_id = d.user_id AND la.document_type = 'privacy_policy') THEN 1 ELSE 0 END AS has_privacy,
		       COALESCE((SELECT la.version FROM legal_acceptances la WHERE la.user_id = d.user_id AND la.document_type = 'driver_terms'), 0) AS acc_driver_terms_ver,
		       COALESCE((SELECT la.version FROM legal_acceptances la WHERE la.user_id = d.user_id AND la.document_type = 'user_terms'), 0) AS acc_user_terms_ver,
		       COALESCE((SELECT la.version FROM legal_acceptances la WHERE la.user_id = d.user_id AND la.document_type = 'privacy_policy'), 0) AS acc_privacy_ver,
		       COALESCE(u.terms_accepted, 0) AS user_terms_accepted,
		       COALESCE(d.terms_accepted, 0) AS driver_terms_legacy
		FROM drivers d
		JOIN users u ON u.id = d.user_id
		ORDER BY d.user_id DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []models.Driver
	for rows.Next() {
		var d models.Driver
		if err := rows.Scan(&d.ID, &d.Name, &d.Phone, &d.CarModel, &d.PlateNumber, &d.PromoBalance, &d.CashBalance, &d.Balance, &d.TotalPaid, &d.VerificationStatus,
			&d.HasDriverTerms, &d.HasUserTerms, &d.HasPrivacy,
			&d.AcceptedDriverTermsVersion, &d.AcceptedUserTermsVersion, &d.AcceptedPrivacyVersion,
			&d.UserTermsAcceptedLegacy, &d.DriverTermsLegacy); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

// ListRidersForAdmin returns riders with user_terms / privacy acceptance against active document versions.
func (r *adminDriverRepo) ListRidersForAdmin(ctx context.Context) ([]models.AdminRiderDTO, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT u.id,
		       u.telegram_id,
		       COALESCE(u.name, '') AS name,
		       COALESCE(u.phone, '') AS phone,
		       CASE WHEN EXISTS(SELECT 1 FROM legal_acceptances la `+legalJoinActive+`
		              WHERE la.user_id = u.id AND la.document_type = 'user_terms') THEN 1 ELSE 0 END AS has_user_terms,
		       CASE WHEN EXISTS(SELECT 1 FROM legal_acceptances la `+legalJoinActive+`
		              WHERE la.user_id = u.id AND la.document_type = 'privacy_policy') THEN 1 ELSE 0 END AS has_privacy,
		       COALESCE((SELECT la.version FROM legal_acceptances la WHERE la.user_id = u.id AND la.document_type = 'user_terms'), 0) AS acc_user_terms_ver,
		       COALESCE((SELECT la.version FROM legal_acceptances la WHERE la.user_id = u.id AND la.document_type = 'privacy_policy'), 0) AS acc_privacy_ver,
		       COALESCE(u.terms_accepted, 0) AS terms_accepted
		FROM users u
		WHERE u.role = 'rider'
		ORDER BY u.id DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []models.AdminRiderDTO
	for rows.Next() {
		var dto models.AdminRiderDTO
		var ut, pr, terms, utVer, prVer int
		if err := rows.Scan(&dto.ID, &dto.TelegramID, &dto.Name, &dto.Phone, &ut, &pr, &utVer, &prVer, &terms); err != nil {
			return nil, err
		}
		dto.UserTermsOK = ut != 0
		dto.PrivacyOK = pr != 0
		dto.UserTermsAcceptedVersion = utVer
		dto.PrivacyPolicyAcceptedVersion = prVer
		dto.LegacyTermsAccepted = terms
		dto.TermsAccepted = terms
		out = append(out, dto)
	}
	return out, rows.Err()
}

// GetDriverByID returns a single driver by user id or nil if not found.
func (r *adminDriverRepo) GetDriverByID(ctx context.Context, id int64) (*models.Driver, error) {
	row := r.db.QueryRowContext(ctx, `
		SELECT u.id AS id,
		       COALESCE(u.name, '') AS name,
		       COALESCE(d.phone, '') AS phone,
		       COALESCE(d.car_type, '') AS car_model,
		       COALESCE(d.plate, '') AS plate_number,
		       d.promo_balance,
		       d.cash_balance,
		       d.balance,
		       d.total_paid,
		       COALESCE(d.verification_status, '') AS verification_status
		FROM drivers d
		JOIN users u ON u.id = d.user_id
		WHERE d.user_id = ?1`, id)
	var d models.Driver
	if err := row.Scan(&d.ID, &d.Name, &d.Phone, &d.CarModel, &d.PlateNumber, &d.PromoBalance, &d.CashBalance, &d.Balance, &d.TotalPaid, &d.VerificationStatus); err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}
	return &d, nil
}

// SetDriverBalance sets total wallet (promo+cash) by adjusting cash_balance only; promo_balance is unchanged.
// Cannot set total below current promo_balance (would require reducing promotional credit — use a dedicated admin flow).
func (r *adminDriverRepo) SetDriverBalance(ctx context.Context, id int64, newBalance int64) error {
	var promo int64
	if err := r.db.QueryRowContext(ctx, `SELECT COALESCE(promo_balance, 0) FROM drivers WHERE user_id = ?1`, id).Scan(&promo); err != nil {
		return err
	}
	if newBalance < promo {
		return fmt.Errorf("total cannot be less than promo_balance (%d); promo credit is not reduced via SetDriverBalance", promo)
	}
	cash := newBalance - promo
	_, err := r.db.ExecContext(ctx, `
		UPDATE drivers SET cash_balance = ?1, balance = ?2,
		  is_active = CASE WHEN ?2 <= 0 THEN 0 ELSE is_active END
		WHERE user_id = ?3`, cash, newBalance, id)
	return err
}

// UpdateVerificationStatus sets verification_status. For "rejected", also clears document file_ids and sets application_step to restart doc upload.
func (r *adminDriverRepo) UpdateVerificationStatus(ctx context.Context, driverUserID int64, status string) error {
	if status == "rejected" {
		_, err := r.db.ExecContext(ctx, `
			UPDATE drivers SET verification_status = 'rejected', license_photo_file_id = NULL, vehicle_doc_file_id = NULL, application_step = 'license_photo', application_admin_sent = 0 WHERE user_id = ?1`, driverUserID)
		return err
	}
	_, err := r.db.ExecContext(ctx, `UPDATE drivers SET verification_status = ?1 WHERE user_id = ?2`, status, driverUserID)
	return err
}

// GetDriverTelegramID returns the Telegram user id for the driver (users.telegram_id).
func (r *adminDriverRepo) GetDriverTelegramID(ctx context.Context, driverUserID int64) (int64, error) {
	var telegramID int64
	err := r.db.QueryRowContext(ctx, `SELECT u.telegram_id FROM users u JOIN drivers d ON d.user_id = u.id WHERE d.user_id = ?1`, driverUserID).Scan(&telegramID)
	return telegramID, err
}

