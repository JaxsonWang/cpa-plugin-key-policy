import { useEffect, useMemo, useState } from "react";
import { useLocation, useNavigate, useParams } from "react-router-dom";
import { fetchCatalog, groupByCatalog } from "../api/models";
import type { CatalogGroup } from "../api/models";
import type { ModelRule } from "../types";
import { useT } from "../i18n";

export function buildPickedModels(
  selected: Iterable<string>,
  modelCase: ReadonlyMap<string, string>,
  initial: ModelRule[],
): ModelRule[] {
  const initialByModel = new Map(initial.map((rule) => [rule.model.toLowerCase(), rule]));
  return Array.from(selected).sort().map((key) => {
    const existing = initialByModel.get(key);
    if (existing) return { ...existing };
    return { model: modelCase.get(key) ?? key };
  });
}

export default function ModelPick() {
  const { id } = useParams<{ id?: string }>();
  const navigate = useNavigate();
  const location = useLocation();
  const t = useT();
  const initial = (location.state as { models?: ModelRule[] } | null)?.models ?? [];
  const backTo = id ? `/keys/${encodeURIComponent(id)}/edit` : "/keys/new";
  const [groups, setGroups] = useState<CatalogGroup[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState("");
  const [query, setQuery] = useState("");
  const [selected, setSelected] = useState<Set<string>>(
    () => new Set(initial.map((rule) => rule.model.toLowerCase())),
  );

  useEffect(() => {
    let alive = true;
    void fetchCatalog()
      .then((catalog) => {
        if (alive) setGroups(groupByCatalog(catalog));
      })
      .catch((reason: unknown) => {
        if (alive) setError(reason instanceof Error ? reason.message : t("picker.loadFailed"));
      })
      .finally(() => {
        if (alive) setLoading(false);
      });
    return () => { alive = false; };
  }, []);

  const modelCase = useMemo(() => {
    const result = new Map<string, string>();
    for (const group of groups) for (const model of group.models) result.set(model.toLowerCase(), model);
    for (const rule of initial) if (!result.has(rule.model.toLowerCase())) result.set(rule.model.toLowerCase(), rule.model);
    return result;
  }, [groups, initial]);

  const rules = useMemo<ModelRule[]>(
    () => buildPickedModels(selected, modelCase, initial),
    [initial, modelCase, selected],
  );

  const filtered = useMemo(() => {
    const needle = query.trim().toLowerCase();
    if (!needle) return groups;
    return groups
      .map((group) => ({
        ...group,
        models: group.models.filter((model) => model.toLowerCase().includes(needle) || group.provider.includes(needle)),
      }))
      .filter((group) => group.models.length > 0);
  }, [groups, query]);

  const toggle = (model: string) => {
    const key = model.toLowerCase();
    setSelected((current) => {
      const next = new Set(current);
      if (next.has(key)) next.delete(key); else next.add(key);
      return next;
    });
  };

  const toggleGroup = (group: CatalogGroup) => {
    const allSelected = group.models.every((model) => selected.has(model.toLowerCase()));
    setSelected((current) => {
      const next = new Set(current);
      for (const model of group.models) {
        if (allSelected) next.delete(model.toLowerCase()); else next.add(model.toLowerCase());
      }
      return next;
    });
  };

  return (
    <div className="model-pick-page">
      <div className="mp-head">
        <button type="button" className="btn sm" onClick={() => navigate(backTo)}>{t("keyUsage.back")}</button>
        <h1>{t("picker.title")}</h1>
        <button
          className="btn primary sm"
          onClick={() => navigate(backTo, { state: { pickedModels: rules } })}
          disabled={loading}
        >
          {t("picker.done", { count: selected.size })}
        </button>
      </div>
      <div className="mp-sub">{t("picker.selected", { count: selected.size })}</div>
      <div className="mp-search">
        <span className="mp-icon">⌕</span>
        <input className="input" placeholder={t("picker.searchPlaceholder")} value={query} onChange={(event) => setQuery(event.target.value)} />
      </div>
      <div className="card mp-list">
        {loading ? (
          <div className="muted">{t("picker.loading")}</div>
        ) : error ? (
          <div className="error">{error}</div>
        ) : filtered.length === 0 ? (
          <div className="muted">{t(groups.length === 0 ? "picker.empty" : "picker.noMatch")}</div>
        ) : filtered.map((group) => {
          const allSelected = group.models.every((model) => selected.has(model.toLowerCase()));
          return (
            <div className="picker-group" key={group.provider}>
              <div className="pg-head">
                <span>{group.provider}</span>
                <span className="pg-actions">
                  <button type="button" className="btn sm" onClick={() => toggleGroup(group)}>
                    {allSelected ? t("picker.clearAll") : t("picker.selectAll")}
                  </button>
                </span>
              </div>
              <div className="pg-models">
                {group.models.map((model) => {
                  const active = selected.has(model.toLowerCase());
                  return (
                    <label key={model.toLowerCase()} className={active ? "active" : ""}>
                      <input type="checkbox" checked={active} onChange={() => toggle(model)} />
                      {model}
                    </label>
                  );
                })}
              </div>
            </div>
          );
        })}
      </div>
    </div>
  );
}
