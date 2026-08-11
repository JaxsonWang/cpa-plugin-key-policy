import { useEffect, useMemo, useState } from "react";
import { fetchCatalog, groupByCatalog } from "../api/models";
import type { CatalogGroup } from "../api/models";
import type { ModelRule } from "../types";
import { useT } from "../i18n";

interface Props {
  initial?: ModelRule[];
  onChange: (rules: ModelRule[]) => void;
}

export default function ModelPicker({ initial, onChange }: Props) {
  const t = useT();
  const [groups, setGroups] = useState<CatalogGroup[]>([]);
  const [loaded, setLoaded] = useState(false);
  const [error, setError] = useState("");
  const [query, setQuery] = useState("");
  const [selected, setSelected] = useState<Set<string>>(
    () => new Set((initial ?? []).map((rule) => rule.model.toLowerCase())),
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
        if (alive) setLoaded(true);
      });
    return () => { alive = false; };
  }, []);

  useEffect(() => {
    if (!loaded) return;
    const originalCase = new Map<string, string>();
    for (const group of groups) {
      for (const model of group.models) originalCase.set(model.toLowerCase(), model);
    }
    for (const rule of initial ?? []) {
      if (!originalCase.has(rule.model.toLowerCase())) originalCase.set(rule.model.toLowerCase(), rule.model);
    }
    onChange(Array.from(selected).sort().map((key) => ({ model: originalCase.get(key) ?? key })));
  }, [groups, initial, loaded, onChange, selected]);

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

  if (!loaded) return <div className="muted">{t("picker.loading")}</div>;
  if (error) return <div className="error">{error}</div>;
  if (groups.length === 0) return <div className="muted">{t("picker.empty")}</div>;

  return (
    <div>
      <input
        className="input"
        placeholder={t("picker.searchPlaceholder")}
        value={query}
        onChange={(event) => setQuery(event.target.value)}
        style={{ marginBottom: 12 }}
      />
      <div className="muted" style={{ marginBottom: 8 }}>
        {t("picker.selected", { count: selected.size })}
      </div>
      {filtered.map((group) => {
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
  );
}
