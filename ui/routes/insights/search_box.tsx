import { useEffect, useRef, useState } from "react";
import { useDebouncedValue } from "../../lib/use_debounced_value";
import "./insights.css";

export function SearchBox({
  query,
  onQueryChange,
}: {
  query: string;
  onQueryChange: (query: string) => void;
}) {
  const [value, setValue] = useState(query);
  const debounced = useDebouncedValue(value, 300);
  const latest = useRef({ query, debounced, onQueryChange });
  latest.current = { query, debounced, onQueryChange };

  // Adopt external changes (Back, links) but not the echo of our own navigation.
  useEffect(() => {
    if (query !== latest.current.debounced) setValue(query);
  }, [query]);

  useEffect(() => {
    if (debounced !== latest.current.query) {
      latest.current.onQueryChange(debounced);
    }
  }, [debounced]);

  return (
    <input
      aria-label="Search insights"
      className="search-input"
      id="insight-search"
      name="query"
      value={value}
      onChange={(event) => setValue(event.target.value)}
      placeholder="Search insights"
    />
  );
}
