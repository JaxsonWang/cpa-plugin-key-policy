import { useCallback, useEffect, useRef, useState } from "react";
import { Link, useNavigate } from "react-router-dom";
import {
  deleteKey,
  listKeys,
  resetAllUsage,
  resetRPM,
  rotateKey,
  setKeyEnabled,
} from "../api/keys";
import type { KeyPublic } from "../types";
import PlainKeyModal from "../components/PlainKeyModal";
import { useT } from "../i18n";

function errorMessage(error: unknown, fallback: string): string {
  const typed = error as {
    response?: { data?: { error?: { message?: string } } };
    message?: string;
  };
  return typed.response?.data?.error?.message ?? typed.message ?? fallback;
}

function replaceKey(keys: KeyPublic[], updated: KeyPublic): KeyPublic[] {
  return keys.map((key) => (key.id === updated.id ? updated : key));
}

export default function KeyList() {
  const t = useT();
  const [keys, setKeys] = useState<KeyPublic[]>([]);
  const [selectedIDs, setSelectedIDs] = useState<Set<string>>(new Set());
  const [updatingIDs, setUpdatingIDs] = useState<Set<string>>(new Set());
  const [resettingUsage, setResettingUsage] = useState(false);
  const [error, setError] = useState("");
  const [loading, setLoading] = useState(true);
  const [plain, setPlain] = useState<string | null>(null);
  const [plainTitle, setPlainTitle] = useState<string>("");

  const load = useCallback(async () => {
    setLoading(true);
    setError("");
    try {
      const nextKeys = await listKeys();
      const liveIDs = new Set(nextKeys.map((key) => key.id));
      setKeys(nextKeys);
      setSelectedIDs((current) => new Set([...current].filter((id) => liveIDs.has(id))));
    } catch (error) {
      setError(errorMessage(error, t("keys.loadFailed")));
    } finally {
      setLoading(false);
    }
  }, [t]);

  useEffect(() => {
    void load();
  }, [load]);

  const onRotate = async (id: string) => {
    if (!confirm(t("keys.rotateConfirm", { id }))) return;
    try {
      const result = await rotateKey(id);
      setPlain(result.plain_key);
      setPlainTitle(t("keys.rotated"));
      void load();
    } catch (error) {
      alert(errorMessage(error, t("keys.rotateFailed")));
    }
  };

  const onReset = async (id: string) => {
    try {
      await resetRPM(id);
      void load();
    } catch (error) {
      alert(errorMessage(error, t("keys.resetFailed")));
    }
  };

  const onResetUsage = async () => {
    if (!confirm(t("keys.resetUsageConfirm"))) return;
    setResettingUsage(true);
    setError("");
    try {
      await resetAllUsage();
      await load();
    } catch (error) {
      setError(errorMessage(error, t("keys.resetUsageFailed")));
    } finally {
      setResettingUsage(false);
    }
  };

  const onDelete = async (id: string) => {
    if (!confirm(t("keys.deleteConfirm", { id }))) return;
    try {
      await deleteKey(id);
      setSelectedIDs((current) => {
        const next = new Set(current);
        next.delete(id);
        return next;
      });
      void load();
    } catch (error) {
      alert(errorMessage(error, t("keys.deleteFailed")));
    }
  };

  const onSetEnabled = async (id: string, enabled: boolean) => {
    setError("");
    setUpdatingIDs((current) => new Set(current).add(id));
    try {
      const updated = await setKeyEnabled(id, enabled);
      setKeys((current) => replaceKey(current, updated));
    } catch (error) {
      setError(`${id}: ${errorMessage(error, t("keys.statusUpdateFailed"))}`);
    } finally {
      setUpdatingIDs((current) => {
        const next = new Set(current);
        next.delete(id);
        return next;
      });
    }
  };

  const onBatchSetEnabled = async (enabled: boolean) => {
    const ids = keys.filter((key) => selectedIDs.has(key.id)).map((key) => key.id);
    if (ids.length === 0) return;

    setError("");
    setUpdatingIDs((current) => new Set([...current, ...ids]));
    const results = await Promise.allSettled(ids.map((id) => setKeyEnabled(id, enabled)));
    const updatedByID = new Map<string, KeyPublic>();
    const failedIDs: string[] = [];

    results.forEach((result, index) => {
      const id = ids[index];
      if (result.status === "fulfilled") updatedByID.set(id, result.value);
      else failedIDs.push(id);
    });

    setKeys((current) => current.map((key) => updatedByID.get(key.id) ?? key));
    setSelectedIDs(new Set(failedIDs));
    setUpdatingIDs((current) => {
      const next = new Set(current);
      ids.forEach((id) => next.delete(id));
      return next;
    });
    if (failedIDs.length > 0) {
      setError(t("keys.batchUpdateFailed", { count: failedIDs.length }));
    }
  };

  const allSelected = keys.length > 0 && selectedIDs.size === keys.length;
  const partiallySelected = selectedIDs.size > 0 && !allSelected;
  const busy = updatingIDs.size > 0 || resettingUsage;

  const onToggleAll = () => {
    setSelectedIDs(allSelected ? new Set() : new Set(keys.map((key) => key.id)));
  };

  const onToggleSelected = (id: string) => {
    setSelectedIDs((current) => {
      const next = new Set(current);
      if (next.has(id)) next.delete(id);
      else next.add(id);
      return next;
    });
  };

  return (
    <div className="key-list-page">
      <div className="fp-head mobile-hidden">
        <h1>{t("header.keyList")}</h1>
        <div className="fp-actions">
          <button className="btn sm" disabled={loading || busy} onClick={load}>
            {t("keys.refresh")}
          </button>
          <button
            className="btn sm danger-outline"
            disabled={loading || busy || keys.length === 0}
            onClick={() => void onResetUsage()}
          >
            {resettingUsage ? t("keys.resettingUsage") : t("keys.resetUsage")}
          </button>
        </div>
      </div>

      {error && <div className="error">{error}</div>}

      {!loading && keys.length > 0 && (
        <div className={`key-bulkbar${selectedIDs.size > 0 ? " active" : ""}`}>
          <label className="key-select-all">
            <SelectionCheckbox
              checked={allSelected}
              indeterminate={partiallySelected}
              ariaLabel={t("keys.selectAll")}
              onChange={onToggleAll}
            />
            <span>{selectedIDs.size > 0
              ? t("keys.selectedCount", { count: selectedIDs.size })
              : t("keys.selectAll")}</span>
          </label>
          <div className="key-bulk-actions">
            <button
              className="btn sm"
              disabled={selectedIDs.size === 0 || busy}
              onClick={() => void onBatchSetEnabled(true)}
            >
              {t("keys.batchEnable")}
            </button>
            <button
              className="btn sm danger-outline"
              disabled={selectedIDs.size === 0 || busy}
              onClick={() => void onBatchSetEnabled(false)}
            >
              {t("keys.batchDisable")}
            </button>
            <button className="btn sm mobile-only" disabled={loading || busy} onClick={load}>
              {t("keys.refresh")}
            </button>
            <button
              className="btn sm danger-outline mobile-only"
              disabled={loading || busy}
              onClick={() => void onResetUsage()}
            >
              {resettingUsage ? t("keys.resettingUsage") : t("keys.resetUsage")}
            </button>
          </div>
        </div>
      )}

      {loading ? (
        <div className="muted">{t("keys.loading")}</div>
      ) : keys.length === 0 ? (
        <div className="card muted">{t("keys.empty")}</div>
      ) : (
        <>
          <KeyTable
            keys={keys}
            selectedIDs={selectedIDs}
            updatingIDs={updatingIDs}
            allSelected={allSelected}
            partiallySelected={partiallySelected}
            onToggleAll={onToggleAll}
            onToggleSelected={onToggleSelected}
            onSetEnabled={onSetEnabled}
            onDelete={onDelete}
            onRotate={onRotate}
            onReset={onReset}
          />
          <div className="card-stack mobile-only">
            {keys.map((key) => (
              <KeyCard
                key={key.id}
                item={key}
                selected={selectedIDs.has(key.id)}
                updating={updatingIDs.has(key.id)}
                onToggleSelected={onToggleSelected}
                onSetEnabled={onSetEnabled}
                onDelete={onDelete}
              />
            ))}
          </div>
        </>
      )}

      <Link to="/keys/new" className="fab" aria-label={t("keys.newKey")}>+</Link>
      <MobileTabBar active="keys" />

      {plain && (
        <PlainKeyModal
          plainKey={plain}
          title={plainTitle}
          onClose={() => setPlain(null)}
        />
      )}
    </div>
  );
}

function SelectionCheckbox({
  checked,
  indeterminate = false,
  ariaLabel,
  onChange,
}: {
  checked: boolean;
  indeterminate?: boolean;
  ariaLabel: string;
  onChange: () => void;
}) {
  const ref = useRef<HTMLInputElement>(null);

  useEffect(() => {
    if (ref.current) ref.current.indeterminate = indeterminate;
  }, [indeterminate]);

  return (
    <input
      ref={ref}
      className="key-checkbox"
      type="checkbox"
      checked={checked}
      aria-label={ariaLabel}
      onChange={onChange}
    />
  );
}

function KeyStatusSwitch({
  item,
  updating,
  onChange,
}: {
  item: KeyPublic;
  updating: boolean;
  onChange: (enabled: boolean) => void;
}) {
  const t = useT();
  return (
    <label
      className="switch key-status-switch"
      title={t("keys.statusToggle", { id: item.id })}
      onClick={(event) => event.stopPropagation()}
      onPointerDown={(event) => event.stopPropagation()}
    >
      <input
        type="checkbox"
        checked={item.enabled}
        disabled={updating}
        aria-label={t("keys.statusToggle", { id: item.id })}
        onChange={(event) => onChange(event.target.checked)}
      />
      <span className="track"><span className="thumb" /></span>
      <span className="key-status-label">
        {updating ? t("keys.updating") : t(item.enabled ? "keys.enabled" : "keys.disabled")}
      </span>
    </label>
  );
}

function KeyTable({
  keys,
  selectedIDs,
  updatingIDs,
  allSelected,
  partiallySelected,
  onToggleAll,
  onToggleSelected,
  onSetEnabled,
  onDelete,
  onRotate,
  onReset,
}: {
  keys: KeyPublic[];
  selectedIDs: Set<string>;
  updatingIDs: Set<string>;
  allSelected: boolean;
  partiallySelected: boolean;
  onToggleAll: () => void;
  onToggleSelected: (id: string) => void;
  onSetEnabled: (id: string, enabled: boolean) => void;
  onDelete: (id: string) => void;
  onRotate: (id: string) => void;
  onReset: (id: string) => void;
}) {
  const t = useT();
  return (
    <div className="card table-wrap key-table mobile-hidden">
      <table>
        <thead>
          <tr>
            <th className="key-select-col">
              <SelectionCheckbox
                checked={allSelected}
                indeterminate={partiallySelected}
                ariaLabel={t("keys.selectAll")}
                onChange={onToggleAll}
              />
            </th>
            <th>{t("keys.colIdName")}</th>
            <th>{t("keys.colPreview")}</th>
            <th>{t("keys.colStatus")}</th>
            <th className="num">{t("keys.colRpm")}</th>
            <th>{t("keys.colUsage")}</th>
            <th className="num">{t("keys.colModels")}</th>
            <th>{t("keys.colActions")}</th>
          </tr>
        </thead>
        <tbody>
          {keys.map((key) => (
            <tr key={key.id} className={key.enabled ? "" : "is-disabled"}>
              <td className="key-select-col">
                <SelectionCheckbox
                  checked={selectedIDs.has(key.id)}
                  ariaLabel={t("keys.selectKey", { id: key.id })}
                  onChange={() => onToggleSelected(key.id)}
                />
              </td>
              <td>
                <Link className="key-name-link" to={`/keys/${encodeURIComponent(key.id)}/usage`}>
                  {key.name || key.id}
                </Link>
                <div className="mono key-id">{key.id}</div>
              </td>
              <td><span className="mono key-preview">{key.key_preview}</span></td>
              <td>
                <KeyStatusSwitch
                  item={key}
                  updating={updatingIDs.has(key.id)}
                  onChange={(enabled) => void onSetEnabled(key.id, enabled)}
                />
              </td>
              <td className="num strong">{key.rpm > 0 ? key.rpm : "∞"}</td>
              <td><UsageCell item={key} /></td>
              <td className="num strong">{key.models?.length ?? 0}</td>
              <td>
                <div className="key-table-actions">
                  <Link className="btn sm" to={`/keys/${encodeURIComponent(key.id)}/usage`}>
                    {t("keys.detail")}
                  </Link>
                  <Link className="btn sm" to={`/keys/${encodeURIComponent(key.id)}/edit`}>
                    {t("keys.edit")}
                  </Link>
                  <button className="btn sm" onClick={() => onReset(key.id)}>{t("keys.resetRpm")}</button>
                  <button className="btn sm" onClick={() => onRotate(key.id)}>{t("keys.rotate")}</button>
                  <button className="btn sm danger-outline" onClick={() => onDelete(key.id)}>{t("keys.delete")}</button>
                </div>
              </td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}

function UsageCell({ item }: { item: KeyPublic }) {
  const t = useT();
  const dailyLimit = item.usage.daily_limit_usd;
  const weeklyLimit = item.usage.weekly_limit_usd;
  const value = (used: number, limit: number) => (
    `$${used.toFixed(2)} / ${limit > 0 ? `$${limit.toFixed(2)}` : t("usage.unlimited")}`
  );
  return (
    <div className="key-usage-cell">
      <span><b>{t("usage.today")}</b>{value(item.usage.daily_usd, dailyLimit)}</span>
      <span><b>{t("usage.thisWeek")}</b>{value(item.usage.weekly_usd, weeklyLimit)}</span>
    </div>
  );
}

function KeyCard({
  item,
  selected,
  updating,
  onToggleSelected,
  onSetEnabled,
  onDelete,
}: {
  item: KeyPublic;
  selected: boolean;
  updating: boolean;
  onToggleSelected: (id: string) => void;
  onSetEnabled: (id: string, enabled: boolean) => void;
  onDelete: (id: string) => void;
}) {
  const t = useT();
  const nav = useNavigate();
  const ref = useRef<HTMLDivElement>(null);
  const [dx, setDx] = useState(0);
  const [revoking, setRevoking] = useState(false);
  const startX = useRef(0);
  const startY = useRef(0);
  const dragging = useRef(false);
  const horizontal = useRef(false);
  const moved = useRef(false);

  const limit = item.usage.daily_limit_usd > 0 ? item.usage.daily_limit_usd : 0;
  const pct = limit > 0 ? Math.min(100, (item.usage.daily_usd / limit) * 100) : 0;
  const over = limit > 0 && item.usage.daily_usd >= limit;
  const modelNames = (item.models ?? []).map((model) => model.model);
  const shownChips = modelNames.slice(0, 2);
  const moreCount = Math.max(0, modelNames.length - 2);

  const onPointerDown = (event: React.PointerEvent) => {
    dragging.current = true;
    horizontal.current = false;
    moved.current = false;
    startX.current = event.clientX;
    startY.current = event.clientY;
    (event.target as HTMLElement).setPointerCapture?.(event.pointerId);
  };
  const onPointerMove = (event: React.PointerEvent) => {
    if (!dragging.current) return;
    const deltaX = event.clientX - startX.current;
    const deltaY = event.clientY - startY.current;
    if (!horizontal.current && Math.abs(deltaX) > 8 && Math.abs(deltaX) > Math.abs(deltaY)) {
      horizontal.current = true;
    }
    if (horizontal.current) {
      event.preventDefault();
      moved.current = true;
      setDx(Math.max(-96, Math.min(0, deltaX)));
    }
  };
  const onPointerUp = () => {
    if (!dragging.current) return;
    dragging.current = false;
    if (horizontal.current) {
      if (dx <= -64) {
        setRevoking(true);
        setDx(-88);
      } else {
        setDx(0);
      }
    }
  };
  const onClick = () => {
    if (moved.current || revoking) return;
    nav(`/keys/${encodeURIComponent(item.id)}/usage`);
  };
  const doRevoke = () => {
    setDx(0);
    setRevoking(false);
    onDelete(item.id);
  };

  return (
    <div
      ref={ref}
      className={`keycard${item.enabled ? "" : " disabled"}${over ? " over" : ""}${selected ? " selected" : ""}`}
      onPointerDown={onPointerDown}
      onPointerMove={onPointerMove}
      onPointerUp={onPointerUp}
      onPointerCancel={onPointerUp}
      onClick={onClick}
      style={{ transform: `translateX(${dx}px)`, touchAction: "pan-y" }}
    >
      <div
        className="kc-revoke"
        style={{ opacity: revoking || dx < -8 ? 1 : 0, transition: "opacity 150ms" }}
        onClick={(event) => {
          event.stopPropagation();
          if (revoking) doRevoke();
        }}
      >
        <span className="revoke-icon">⊘</span>
        <span>{t("keys.mobile.revoke")}</span>
      </div>
      <div className="kc-head">
        <span
          className="kc-select"
          onClick={(event) => event.stopPropagation()}
          onPointerDown={(event) => event.stopPropagation()}
        >
          <SelectionCheckbox
            checked={selected}
            ariaLabel={t("keys.selectKey", { id: item.id })}
            onChange={() => onToggleSelected(item.id)}
          />
        </span>
        <span className="kc-dot" />
        <span className="kc-name">{item.name || item.id}</span>
        <KeyStatusSwitch
          item={item}
          updating={updating}
          onChange={(enabled) => onSetEnabled(item.id, enabled)}
        />
        <span className="kc-chevron">›</span>
      </div>
      <div className="kc-preview">{item.key_preview}</div>
      {limit > 0 && (
        <>
          <div className="kc-bar"><span style={{ width: `${pct}%` }} /></div>
          <div className="kc-meta">
            <span>${item.usage.daily_usd.toFixed(2)} / ${limit.toFixed(2)}</span>
            <span>{modelNames.length} {t("keys.mobile.modelsSuffix")}</span>
          </div>
        </>
      )}
      {limit === 0 && (
        <div className="kc-meta">
          <span>${item.usage.daily_usd.toFixed(2)} · {t("keys.mobile.noLimit")}</span>
          <span>{modelNames.length} {t("keys.mobile.modelsSuffix")}</span>
        </div>
      )}
      {shownChips.length > 0 && (
        <div className="kc-chips">
          {shownChips.map((model) => <span key={model} className="chip">{model}</span>)}
          {moreCount > 0 && <span className="chip more">+{moreCount}</span>}
        </div>
      )}
    </div>
  );
}

export function MobileFormHeader({ title, backTo }: { title: string; backTo: string }) {
  const t = useT();
  return (
    <div className="mobile-form-header mobile-only">
      <Link to={backTo} className="mfb-back">
        {t("keyUsage.back")}
      </Link>
      <h2 className="mfb-title">{title}</h2>
    </div>
  );
}

export function MobileTabBar({
  active,
  showUsage = false,
  usagePath = "/keys",
}: {
  active: "keys" | "usage" | "new";
  showUsage?: boolean;
  usagePath?: string;
}) {
  const t = useT();
  const nav = useNavigate();
  const tab = (id: "keys" | "usage" | "new", label: string, icon: string, target: string) => (
    <button
      className={`tab${active === id ? " active" : ""}`}
      onClick={() => nav(target)}
    >
      <span className="tab-icon">{icon}</span>
      <span>{label}</span>
    </button>
  );
  return (
    <nav className={`tabbar${showUsage ? "" : " tabbar--no-usage"}`}>
      {tab("keys", t("keys.mobile.tabKeys"), "#", "/keys")}
      {showUsage && tab("usage", t("keys.mobile.tabUsage"), "#", usagePath)}
      {tab("new", t("keys.mobile.tabNew"), "+", "/keys/new")}
    </nav>
  );
}
