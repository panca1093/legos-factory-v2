import { defineStore } from "pinia";
import { useFactory } from "./factory";
import { api, type User } from "../types/api";

export const useSession = defineStore("session", {
  state: () => ({
    user: null as User | null,
    mode: "dev" as string,
    loading: true,
  }),
  getters: {
    canWrite: (s) => !!s.user && s.user.role !== "VIEWER",
    isAdmin: (s) =>
      !!s.user && (s.user.role === "OWNER" || s.user.role === "ADMIN"),
  },
  actions: {
    async bootstrap() {
      try {
        const res = await api.get<{ user: User; mode: string }>(
          "/api/v1/auth/session",
        );
        this.user = res.user;
        this.mode = res.mode;
      } catch {
        // not signed in: try dev login (404s cleanly in oidc mode)
        try {
          const res = await api.post<{ user: User; mode: string }>(
            "/api/v1/auth/dev-login",
          );
          this.user = res.user;
          this.mode = res.mode;
        } catch {
          this.user = null;
          this.mode = "oidc";
        }
      } finally {
        this.loading = false;
      }
    },
    async logout() {
      await api.post("/api/v1/auth/logout");
      this.user = null;
      const factory = useFactory();
      factory.stopLive();
      factory.$reset();
    },
  },
});
