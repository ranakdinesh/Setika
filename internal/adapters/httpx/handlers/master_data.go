package handlers

import (
	"database/sql"
	"encoding/json"
	"net/http"
)

type masterCountryResponse struct {
	Code              string  `json:"code"`
	Name              string  `json:"name"`
	CurrencyCode      string  `json:"currency_code"`
	CurrencyName      string  `json:"currency_name"`
	CurrencySymbol    string  `json:"currency_symbol"`
	FlagEmoji         string  `json:"flag_emoji"`
	DefaultTimezoneID *string `json:"default_timezone_id,omitempty"`
}

type masterTimezoneResponse struct {
	ID               string `json:"id"`
	DisplayName      string `json:"display_name"`
	Region           string `json:"region"`
	UTCOffsetMinutes int32  `json:"utc_offset_minutes"`
	UTCOffset        string `json:"utc_offset"`
}

func (h *Handler) MasterCountries(w http.ResponseWriter, r *http.Request) {
	if h == nil || h.db == nil {
		writeError(w, http.StatusInternalServerError, "master data handler is not configured")
		return
	}
	rows, err := h.db.Query(r.Context(), `
		SELECT
			iso_alpha2,
			name,
			currency_code,
			currency_name,
			currency_symbol,
			flag_emoji,
			NULLIF(default_timezone_id, '')
		FROM platform.countries
		WHERE is_active = TRUE
		ORDER BY CASE WHEN iso_alpha2 = 'IN' THEN 0 ELSE 1 END, name ASC
	`)
	if err != nil {
		if h.log != nil {
			h.log.Error(r.Context()).Err(err).Str("operation", "master countries").Msg("country master data load failed")
		}
		writeError(w, http.StatusInternalServerError, "failed to load countries")
		return
	}
	defer rows.Close()

	countries := make([]masterCountryResponse, 0)
	for rows.Next() {
		var country masterCountryResponse
		var defaultTimezone sql.NullString
		if err := rows.Scan(
			&country.Code,
			&country.Name,
			&country.CurrencyCode,
			&country.CurrencyName,
			&country.CurrencySymbol,
			&country.FlagEmoji,
			&defaultTimezone,
		); err != nil {
			if h.log != nil {
				h.log.Error(r.Context()).Err(err).Str("operation", "master countries scan").Msg("country master data scan failed")
			}
			writeError(w, http.StatusInternalServerError, "failed to load countries")
			return
		}
		if defaultTimezone.Valid {
			country.DefaultTimezoneID = &defaultTimezone.String
		}
		countries = append(countries, country)
	}
	if err := rows.Err(); err != nil {
		if h.log != nil {
			h.log.Error(r.Context()).Err(err).Str("operation", "master countries rows").Msg("country master data rows failed")
		}
		writeError(w, http.StatusInternalServerError, "failed to load countries")
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(countries)
}

func (h *Handler) MasterTimezones(w http.ResponseWriter, r *http.Request) {
	if h == nil || h.db == nil {
		writeError(w, http.StatusInternalServerError, "master data handler is not configured")
		return
	}
	rows, err := h.db.Query(r.Context(), `
		SELECT
			id,
			display_name,
			region,
			utc_offset_minutes,
			utc_offset
		FROM platform.timezones
		WHERE is_active = TRUE
		ORDER BY CASE WHEN id = 'Asia/Kolkata' THEN 0 ELSE 1 END, region ASC, utc_offset_minutes ASC, display_name ASC
	`)
	if err != nil {
		if h.log != nil {
			h.log.Error(r.Context()).Err(err).Str("operation", "master timezones").Msg("timezone master data load failed")
		}
		writeError(w, http.StatusInternalServerError, "failed to load timezones")
		return
	}
	defer rows.Close()

	timezones := make([]masterTimezoneResponse, 0)
	for rows.Next() {
		var timezone masterTimezoneResponse
		if err := rows.Scan(
			&timezone.ID,
			&timezone.DisplayName,
			&timezone.Region,
			&timezone.UTCOffsetMinutes,
			&timezone.UTCOffset,
		); err != nil {
			if h.log != nil {
				h.log.Error(r.Context()).Err(err).Str("operation", "master timezones scan").Msg("timezone master data scan failed")
			}
			writeError(w, http.StatusInternalServerError, "failed to load timezones")
			return
		}
		timezones = append(timezones, timezone)
	}
	if err := rows.Err(); err != nil {
		if h.log != nil {
			h.log.Error(r.Context()).Err(err).Str("operation", "master timezones rows").Msg("timezone master data rows failed")
		}
		writeError(w, http.StatusInternalServerError, "failed to load timezones")
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(timezones)
}
