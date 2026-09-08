import { useMemo } from "react";
import { createApi } from "./api";
import { useSession } from "./session";

/** API client bound to the current session token. */
export function useApi() {
  const { token } = useSession();
  return useMemo(() => createApi(token), [token]);
}
