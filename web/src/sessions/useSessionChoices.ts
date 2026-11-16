import { useEffect, useState } from "react";
import { message } from "../text";
import { sessionAPI } from "./api";
import type { AISession, SessionFilter, SessionListPage } from "./types";

// Selectors need every cursor window, including sessions beyond the first 100.
// Never expose a partial set after a failed or superseded request.
export function useSessionChoices({ engine, archived = false, q }: Pick<SessionFilter, "engine" | "archived" | "q">, revision: string) {
  const [state, setState] = useState<{data:AISession[]; loading:boolean; error:string}>({data:[],loading:true,error:""});
  const [retry, setRetry] = useState(0);
  useEffect(() => {
    const controller = new AbortController();
    setState({data:[],loading:true,error:""});
    void (async () => {
      try {
        const items:AISession[] = [], cursors = new Set<string>();
        let cursor: string | null = null;
        do {
          const page: SessionListPage = await sessionAPI.list({engine,q,archived,limit:100,cursor},{signal:controller.signal});
          items.push(...page.items);
          cursor=page.next_cursor;
          if (cursor && cursors.has(cursor)) throw new Error("Unable to load sessions. Please try again.");
          if (cursor) cursors.add(cursor);
        } while (cursor && !controller.signal.aborted);
        if (!controller.signal.aborted) setState({data:items,loading:false,error:""});
      } catch (error) { if (!controller.signal.aborted) setState({data:[],loading:false,error:message(error)}); }
    })();
    return () => controller.abort();
  },[engine,archived,q,revision,retry]);
  return {...state,reload:()=>setRetry(value=>value+1)};
}
