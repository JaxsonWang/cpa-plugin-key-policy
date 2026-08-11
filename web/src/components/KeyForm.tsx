import { Fragment, useCallback, useEffect, useState, type ReactNode } from "react";
import { useNavigate } from "react-router-dom";
import type { KeyPublic, ModelRule } from "../types";
import ModelPicker from "./ModelPicker";
import { getPriceTable, lookupPrice, type PriceTable } from "../store/modelPrices";
import { useT } from "../i18n";

export interface KeyFormValues {
  id: string;
  name: string;
  enabled: boolean;
  rpm: number;
  models: ModelRule[];
  daily_limit_usd: number;
  weekly_limit_usd: number;
  // Per-key override for GET /v1/models. CPA cannot filter the model list per
  // downstream key, so the only plugin-enforceable choice is binary: 401 (hide
  // the list) or allow (client sees the full global list). Default false.
  allow_models_endpoint?: boolean;
}

interface Props {
  initial?: KeyPublic;
  idReadOnly?: boolean;
  submitLabel: string;
  onSubmit: (v: KeyFormValues) => Promise<void>;
  onCancel: () => void;
  // top-level error to render
  error?: string;
  // route path for the standalone model-picker page (e.g. "/keys/new/models").
  // When set, the desktop form renders a chip box + "add model" button that
  // navigates here with the current models as router state. The picker page
  // navigates back with state.pickedModels, which the parent merges into
  // `initial` before re-rendering this form.
  pickPath?: string;
  // extra-danger button config for the footer (edit mode). When provided,
  // renders a danger-outline button on the far right of the footer.
  dangerLabel?: string;
  onDanger?: () => void;
}

// Pricing for a single model, kept in form state alongside the selection.
interface PriceRow {
  input_price_per_million: number;
  output_price_per_million: number;
  cache_read_price_per_million: number;
  // per-call billing toggle + fixed charge. billing_mode "tokens" (default)
  // uses the three token prices; "per_call" uses per_call_usd per successful
  // request and ignores the token prices (kept dormant for round-tripping).
  billing_mode: "tokens" | "per_call";
  per_call_usd: number;
}

function priceKey(m: { model: string }): string {
  return m.model.toLowerCase();
}

function parseNum(value: string): number {
  const n = parseFloat(value);
  return Number.isFinite(n) ? n : 0;
}

export default function KeyForm({
  initial,
  idReadOnly,
  submitLabel,
  onSubmit,
  onCancel,
  error,
  pickPath,
  dangerLabel,
  onDanger,
}: Props) {
  const nav = useNavigate();
  const [id, setId] = useState(initial?.id ?? "");
  const [name, setName] = useState(initial?.name ?? "");
  const [enabled, setEnabled] = useState(initial?.enabled ?? true);
  const [rpm, setRpm] = useState(initial?.rpm ?? 0);
  const [dailyLimit, setDailyLimit] = useState(initial?.daily_limit_usd ?? 0);
  const [weeklyLimit, setWeeklyLimit] = useState(initial?.weekly_limit_usd ?? 0);
  const [allowModels, setAllowModels] = useState<boolean>(initial?.allow_models_endpoint ?? false);
  const t = useT();

  // Pricing table keyed by exact model name (case-insensitive) so it survives
  // picker re-emits.
  const [prices, setPrices] = useState<Record<string, PriceRow>>(() => {
    const out: Record<string, PriceRow> = {};
    for (const m of initial?.models ?? []) {
      out[priceKey(m)] = {
        input_price_per_million: m.input_price_per_million ?? 0,
        output_price_per_million: m.output_price_per_million ?? 0,
        cache_read_price_per_million: m.cache_read_price_per_million ?? 0,
        billing_mode: m.billing_mode === "per_call" ? "per_call" : "tokens",
        per_call_usd: m.per_call_usd ?? 0,
      };
    }
    return out;
  });
  const [models, setModels] = useState<ModelRule[]>(initial?.models ?? []);
  const [busy, setBusy] = useState(false);
  const [localErr, setLocalErr] = useState("");
  const [expandedPrice, setExpandedPrice] = useState<Record<string, boolean>>({});

  // LiteLLM price hints (community price table). Loaded once on mount, silent
  // failure: if null/inflight, the per-row "recommend" affordance simply isn't
  // rendered. The form is fully usable without it. Never auto-fills prices —
  // the user must click "recommend" per row (replace semantics, overwrites
  // whatever was in that row).
  const [priceTable, setPriceTable] = useState<PriceTable | null>(null);
  useEffect(() => {
    let alive = true;
    void getPriceTable().then((t) => {
      if (alive) setPriceTable(t);
    });
    return () => {
      alive = false;
    };
  }, []);

  // ModelPicker emits fresh ModelRule[] on every selection change (and once
  // when the catalog finishes loading). We must NOT let those re-emits wipe
  // pricing the user already typed: when merging, preserve existing rows and
  // only (a) add empty rows for newly-selected models, (b) drop rows for
  // models that are no longer selected. Keys already present are copied
  // through untouched. Wrapped in useCallback so ModelPicker's emit effect
  // does not re-fire on every KeyForm re-render (which would otherwise loop
  // and risk dropping mid-typing values).
  const handleModelsChange = useCallback((next: ModelRule[]) => {
    setModels(next);
    setPrices((prev) => {
      const updated: Record<string, PriceRow> = {};
      for (const m of next) {
        const key = priceKey(m);
        updated[key] = prev[key] ?? { input_price_per_million: 0, output_price_per_million: 0, cache_read_price_per_million: 0, billing_mode: "tokens", per_call_usd: 0 };
      }
      // Rows for models no longer selected simply aren't copied.
      return updated;
    });
  }, []);

  const setPrice = (m: ModelRule, field: keyof PriceRow, value: string) => {
    const key = priceKey(m);
    setPrices((prev) => ({
      ...prev,
      [key]: {
        ...(prev[key] ?? { input_price_per_million: 0, output_price_per_million: 0, cache_read_price_per_million: 0, billing_mode: "tokens", per_call_usd: 0 }),
        [field]: field === "billing_mode" ? (value === "per_call" ? "per_call" : "tokens") : parseNum(value),
      },
    }));
  };

  // One-click fill this row from LiteLLM community prices. Replace semantics:
  // overwrites all three fields (even non-zero user-entered ones). Lookup is by
  // the real model id; the price writes back to this model's row.
  const recommend = (m: ModelRule) => {
    const row = lookupPrice(priceTable, m.model);
    if (!row) return;
    const key = priceKey(m);
    setPrices((prev) => ({
      ...prev,
      [key]: {
        input_price_per_million: row.input_price_per_million,
        output_price_per_million: row.output_price_per_million,
        cache_read_price_per_million: row.cache_read_price_per_million,
        // Recommend fills token prices; it does not change the billing mode. If
        // the row was on per_call, keep it (the recommended token prices stay
        // dormant until the user switches back to tokens).
        billing_mode: prev[key]?.billing_mode ?? "tokens",
        per_call_usd: prev[key]?.per_call_usd ?? 0,
      },
    }));
  };

  const submit = async (e: React.FormEvent) => {
    e.preventDefault();
    setLocalErr("");
    if (!id.trim()) {
      setLocalErr(t("keyForm.idRequired"));
      return;
    }
    // Stamp the per-model pricing back onto the model rules before submit.
    const pricedModels: ModelRule[] = models.map((m) => {
      const row = prices[priceKey(m)];
      return {
        ...m,
        input_price_per_million: row?.input_price_per_million ?? 0,
        output_price_per_million: row?.output_price_per_million ?? 0,
        cache_read_price_per_million: row?.cache_read_price_per_million ?? 0,
        billing_mode: row?.billing_mode === "per_call" ? "per_call" : "tokens",
        per_call_usd: row?.per_call_usd ?? 0,
      };
    });
    setBusy(true);
    try {
      await onSubmit({
        id: id.trim(),
        name: name.trim(),
        enabled,
        rpm,
        models: pricedModels,
        daily_limit_usd: dailyLimit,
        weekly_limit_usd: weeklyLimit,
        allow_models_endpoint: allowModels,
      });
    } catch (err) {
      const e = err as { response?: { data?: { error?: { message?: string } } }; message?: string };
      setLocalErr(e.response?.data?.error?.message ?? e.message ?? t("keyForm.submitFailed"));
    } finally {
      setBusy(false);
    }
  };

  const toggleExpanded = (key: string) => {
    setExpandedPrice((prev) => ({ ...prev, [key]: !prev[key] }));
  };

  const renderPriceEditor = (m: ModelRule, layout: "table" | "mobile") => {
    const key = priceKey(m);
    const row = prices[key] ?? {
      input_price_per_million: 0,
      output_price_per_million: 0,
      cache_read_price_per_million: 0,
      billing_mode: "tokens" as const,
      per_call_usd: 0,
    };
    const perCall = row.billing_mode === "per_call";
    const hint = lookupPrice(priceTable, m.model);

    if (layout === "mobile") {
      return (
        <div className="kf-mprice-body">
          <div className="seg kf-billing-seg" role="group" aria-label={t("keyForm.colBillingMode")}>
            <button
              type="button"
              className={"seg-btn" + (perCall ? "" : " active")}
              onClick={() => setPrice(m, "billing_mode", "tokens")}
            >
              {t("keyForm.billingTokens")}
            </button>
            <button
              type="button"
              className={"seg-btn" + (perCall ? " active" : "")}
              onClick={() => setPrice(m, "billing_mode", "per_call")}
            >
              {t("keyForm.billingPerCall")}
            </button>
          </div>
          {perCall ? (
            <div className="form-row">
              <label>{t("keyForm.colPerCall")}</label>
              <input
                className="input"
                type="number"
                min={0}
                step="0.0001"
                value={row.per_call_usd}
                onChange={(e) => setPrice(m, "per_call_usd", e.target.value)}
              />
            </div>
          ) : (
            <>
              <div className="form-row">
                <label>{t("keyForm.colInput")}</label>
                <input
                  className="input"
                  type="number"
                  min={0}
                  step="0.01"
                  value={row.input_price_per_million}
                  onChange={(e) => setPrice(m, "input_price_per_million", e.target.value)}
                />
              </div>
              <div className="form-row">
                <label>{t("keyForm.colOutput")}</label>
                <input
                  className="input"
                  type="number"
                  min={0}
                  step="0.01"
                  value={row.output_price_per_million}
                  onChange={(e) => setPrice(m, "output_price_per_million", e.target.value)}
                />
              </div>
              <div className="form-row">
                <label title={t("keyForm.colCacheReadHint")}>{t("keyForm.colCacheRead")}</label>
                <input
                  className="input"
                  type="number"
                  min={0}
                  step="0.01"
                  value={row.cache_read_price_per_million}
                  onChange={(e) => setPrice(m, "cache_read_price_per_million", e.target.value)}
                />
              </div>
              {hint && (
                <button
                  type="button"
                  className="btn sm"
                  onClick={() => recommend(m)}
                  title={t("keyForm.recommendTitle")}
                >
                  {t("keyForm.recommend")}
                </button>
              )}
            </>
          )}
          {perCall && row.per_call_usd === 0 && (
            <p className="muted kf-warn">⚠ {t("keyForm.perCallZeroWarn")}</p>
          )}
          {perCall && <p className="muted kf-warn">⚠ {t("keyForm.perCallImageWarn")}</p>}
        </div>
      );
    }

    return (
      <Fragment key={key}>
        <tr>
          <td className="mono">{m.model}</td>
          <td>
            <label className="switch" title={t("keyForm.billingModeTitle")}>
              <input
                type="checkbox"
                checked={perCall}
                onChange={(e) => setPrice(m, "billing_mode", e.target.checked ? "per_call" : "tokens")}
              />
              <span className="track"><span className="thumb" /></span>
              <span>{perCall ? t("keyForm.billingPerCall") : t("keyForm.billingTokens")}</span>
            </label>
          </td>
          {perCall ? (
            <td colSpan={3}>
              <div className="form-row" style={{ marginBottom: 0 }}>
                <label>{t("keyForm.colPerCall")}</label>
                <input
                  className="input"
                  type="number"
                  min={0}
                  step="0.0001"
                  value={row.per_call_usd}
                  onChange={(e) => setPrice(m, "per_call_usd", e.target.value)}
                />
              </div>
            </td>
          ) : (
            <>
              <td>
                <input
                  className="input"
                  type="number"
                  min={0}
                  step="0.01"
                  value={row.input_price_per_million}
                  onChange={(e) => setPrice(m, "input_price_per_million", e.target.value)}
                />
              </td>
              <td>
                <input
                  className="input"
                  type="number"
                  min={0}
                  step="0.01"
                  value={row.output_price_per_million}
                  onChange={(e) => setPrice(m, "output_price_per_million", e.target.value)}
                />
              </td>
              <td>
                <input
                  className="input"
                  type="number"
                  min={0}
                  step="0.01"
                  value={row.cache_read_price_per_million}
                  onChange={(e) => setPrice(m, "cache_read_price_per_million", e.target.value)}
                />
              </td>
            </>
          )}
          <td>
            {!perCall && hint && (
              <button
                type="button"
                className="btn sm"
                onClick={() => recommend(m)}
                title={t("keyForm.recommendTitle")}
              >
                {t("keyForm.recommend")}
              </button>
            )}
          </td>
        </tr>
        {perCall && row.per_call_usd === 0 && (
          <tr className="muted">
            <td colSpan={6} style={{ fontSize: "0.85em" }}>
              ⚠ {t("keyForm.perCallZeroWarn")}
            </td>
          </tr>
        )}
        {perCall && (
          <tr className="muted">
            <td colSpan={6} style={{ fontSize: "0.85em" }}>
              ⚠ {t("keyForm.perCallImageWarn")}
            </td>
          </tr>
        )}
      </Fragment>
    );
  };

  const section = (title: string, children: ReactNode) => (
    <section className="kf-section mobile-only">
      <div className="section-label">{title}</div>
      <div className="kf-section-card">{children}</div>
    </section>
  );

  return (
    <form className="card key-form" onSubmit={submit}>
      <div className="mobile-only kf-sections">
        {section(t("keyForm.mobile.sectionBasic"), (
          <>
            <div className="form-row">
              <label>{t("keyForm.idLabel")}</label>
              <input
                className={"input" + (idReadOnly ? " mono" : "")}
                value={id}
                onChange={(e) => setId(e.target.value)}
                readOnly={idReadOnly}
                placeholder={t("keyForm.idPlaceholder")}
                autoFocus={!idReadOnly}
              />
            </div>
            <div className="form-row">
              <label>{t("keyForm.nameLabel")}</label>
              <input
                className="input"
                value={name}
                onChange={(e) => setName(e.target.value)}
                placeholder={t("keyForm.namePlaceholder")}
              />
            </div>
            <div className="form-row kf-switch-row">
              <label className="switch">
                <input type="checkbox" checked={enabled} onChange={(e) => setEnabled(e.target.checked)} />
                <span className="track"><span className="thumb" /></span>
                <span>{t("keyForm.enableKey")}</span>
              </label>
            </div>
            <div className="form-row">
              <label>{t("keyForm.rpmLabel")}</label>
              <input
                className="input"
                type="number"
                min={0}
                value={rpm}
                onChange={(e) => setRpm(parseInt(e.target.value || "0", 10) || 0)}
              />
            </div>
          </>
        ))}
        {section(t("keyForm.mobile.sectionLimits"), (
          <>
            <div className="form-row">
              <label>{t("keyForm.dailyLimitLabel")}</label>
              <input
                className="input"
                type="number"
                min={0}
                step="0.01"
                value={dailyLimit}
                onChange={(e) => setDailyLimit(parseNum(e.target.value))}
              />
            </div>
            <div className="form-row">
              <label>{t("keyForm.weeklyLimitLabel")}</label>
              <input
                className="input"
                type="number"
                min={0}
                step="0.01"
                value={weeklyLimit}
                onChange={(e) => setWeeklyLimit(parseNum(e.target.value))}
              />
            </div>
          </>
        ))}
        {section(t("keyForm.mobile.sectionAccess"), (
          <>
            <label className="switch kf-access-switch" title={t("keyForm.allowModelsTitle")}>
              <input type="checkbox" checked={allowModels} onChange={(e) => setAllowModels(e.target.checked)} />
              <span className="track"><span className="thumb" /></span>
              <span>{t("keyForm.allowModelsLabel")}</span>
            </label>
            <p className="muted kf-hint">{t("keyForm.allowModelsHint")}</p>
          </>
        ))}
        <section className="kf-section mobile-only">
          <div className="section-label">{t("keyForm.mobile.sectionModels")}</div>
          <div className="form-row" style={{ marginBottom: 12 }}>
            {pickPath ? (
              <div className="model-chips-box">
                {models.length === 0 && <span className="mc-empty">{t("keyForm.modelsEmpty")}</span>}
                {models.map((m) => (
                  <span key={priceKey(m)} className="mc-chip">
                    {m.model}
                    <button type="button" className="mc-x" onClick={() => {
                      setModels((prev) => prev.filter((x) => priceKey(x) !== priceKey(m)));
                    }} aria-label={t("keyForm.removeModel")}>×</button>
                  </span>
                ))}
                <button type="button" className="mc-add" onClick={() => nav(pickPath, { state: { models } })}>
                  + {t("keyForm.addModel")}
                </button>
              </div>
            ) : (
              <ModelPicker initial={initial?.models} onChange={handleModelsChange} />
            )}
          </div>
          {models.length > 0 && (
            <div className="kf-model-list">
              {models.map((m) => {
                const key = priceKey(m);
                const row = prices[key];
                const perCall = row?.billing_mode === "per_call";
                const open = !!expandedPrice[key];
                return (
                  <div key={key} className="kf-model-card">
                    <button
                      type="button"
                      className="kf-model-head"
                      onClick={() => toggleExpanded(key)}
                      aria-expanded={open}
                    >
                      <div>
                        <div className="kf-model-name">{m.model}</div>
                      </div>
                      <span className={"mm-badge" + (perCall ? " per_call" : "")}>
                        {perCall ? t("keyForm.billingPerCall") : t("keyForm.billingTokens")}
                      </span>
                      <span className="kf-chevron">{open ? "▾" : "▸"}</span>
                    </button>
                    {open && renderPriceEditor(m, "mobile")}
                  </div>
                );
              })}
            </div>
          )}
          <p className="muted kf-hint" style={{ marginTop: 8 }}>{t("keyForm.priceLabel")}</p>
        </section>
      </div>

      <div className="mobile-hidden">
      <div className="row2">
        <div className="form-row">
          <label>{t("keyForm.idLabel")}</label>
          <input
            className="input"
            value={id}
            onChange={(e) => setId(e.target.value)}
            readOnly={idReadOnly}
            placeholder={t("keyForm.idPlaceholder")}
            autoFocus={!idReadOnly}
          />
        </div>
        <div className="form-row">
          <label>{t("keyForm.nameLabel")}</label>
          <input
            className="input"
            value={name}
            onChange={(e) => setName(e.target.value)}
            placeholder={t("keyForm.namePlaceholder")}
          />
        </div>
      </div>
      <div className="row2">
        <div className="form-row">
          <label>{t("keyForm.rpmLabel")}</label>
          <input
            className="input"
            type="number"
            min={0}
            value={rpm}
            onChange={(e) => setRpm(parseInt(e.target.value || "0", 10) || 0)}
          />
        </div>
        <div className="form-row">
          <label>{t("keyForm.statusLabel")}</label>
          <label className="switch">
            <input
              type="checkbox"
              checked={enabled}
              onChange={(e) => setEnabled(e.target.checked)}
            />
            <span className="track"><span className="thumb" /></span>
            <span>{t("keyForm.enableKey")}</span>
          </label>
        </div>
      </div>

      <div className="row2">
        <div className="form-row">
          <label>{t("keyForm.dailyLimitLabel")}</label>
          <input
            className="input"
            type="number"
            min={0}
            step="0.01"
            value={dailyLimit}
            onChange={(e) => setDailyLimit(parseNum(e.target.value))}
          />
        </div>
        <div className="form-row">
          <label>{t("keyForm.weeklyLimitLabel")}</label>
          <input
            className="input"
            type="number"
            min={0}
            step="0.01"
            value={weeklyLimit}
            onChange={(e) => setWeeklyLimit(parseNum(e.target.value))}
          />
        </div>
      </div>

      <div className="form-row">
        <label className="switch" title={t("keyForm.allowModelsTitle")}>
          <input
            type="checkbox"
            checked={allowModels}
            onChange={(e) => setAllowModels(e.target.checked)}
          />
          <span className="track"><span className="thumb" /></span>
          <span>{t("keyForm.allowModelsLabel")}</span>
        </label>
        <span className="muted" style={{ fontSize: "0.85em", marginLeft: 8 }}>
          {t("keyForm.allowModelsHint")}
        </span>
      </div>

      <div className="form-row">
        <label>{t("keyForm.modelsLabel")}</label>
        {pickPath ? (
          <div className="model-chips-box">
            {models.length === 0 && <span className="mc-empty">{t("keyForm.modelsEmpty")}</span>}
            {models.map((m) => (
              <span key={priceKey(m)} className="mc-chip">
                {m.model}
                <button type="button" className="mc-x" onClick={() => {
                  setModels((prev) => prev.filter((x) => priceKey(x) !== priceKey(m)));
                }} aria-label={t("keyForm.removeModel")}>×</button>
              </span>
            ))}
            <button type="button" className="mc-add" onClick={() => nav(pickPath, { state: { models } })}>
              + {t("keyForm.addModel")}
            </button>
          </div>
        ) : (
          <ModelPicker initial={initial?.models} onChange={handleModelsChange} />
        )}
      </div>

      {/* Per-model pricing table. Stamped onto each ModelRule at submit.
          Each row toggles between token pricing (default) and per-call fixed
          pricing. Under per_call the three token-price inputs are hidden
          (values retained but dormant) and a single $/call input is shown. */}
      {models.length > 0 && (
        <div className="form-row" style={{ marginTop: 8 }}>
          <label>{t("keyForm.priceLabel")}</label>
          <div className="card table-wrap" style={{ padding: 0 }}>
            <table>
              <thead>
                <tr>
                  <th>{t("keyForm.colModel")}</th>
                  <th title={t("keyForm.colBillingModeHint")}>{t("keyForm.colBillingMode")}</th>
                  <th>{t("keyForm.colInput")}</th>
                  <th>{t("keyForm.colOutput")}</th>
                  <th title={t("keyForm.colCacheReadHint")}>{t("keyForm.colCacheRead")}</th>
                  <th title={t("keyForm.colRecommendHint")}>{t("keyForm.colRecommend")}</th>
                </tr>
              </thead>
              <tbody>
                {models.map((m) => renderPriceEditor(m, "table"))}
              </tbody>
            </table>
          </div>
        </div>
      )}
      </div>

      {(localErr || error) && <div className="error">{localErr || error}</div>}

      <div className="actions fp-foot">
        <button className="btn primary" type="submit" disabled={busy}>
          {busy ? t("keyForm.submitting") : submitLabel}
        </button>
        <button className="btn" type="button" onClick={onCancel}>{t("keyForm.cancel")}</button>
        {dangerLabel && onDanger && (
          <span className="fp-foot-right">
            <button type="button" className="btn danger-outline" onClick={onDanger}>{dangerLabel}</button>
          </span>
        )}
      </div>
    </form>
  );
}
