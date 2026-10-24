<script setup lang="ts">
import { onBeforeUnmount, onMounted, watch } from "vue";
import { RouterView } from "vue-router";
import { useSession } from "../stores/session";
import { useFactory } from "../stores/factory";
const session = useSession();
const factory = useFactory();
const stop = watch(
  () => session.user,
  (user) => {
    if (user) factory.startLive();
    else factory.stopLive();
  },
);
function unauthorized() {
  session.expire();
}
onMounted(() => {
  window.addEventListener("factory:unauthorized", unauthorized);
  void session.bootstrap();
});
onBeforeUnmount(() => {
  window.removeEventListener("factory:unauthorized", unauthorized);
  stop();
  factory.stopLive();
});
</script>
<template>
  <main v-if="session.loading" class="auth-screen" aria-busy="true">
    <h1>Starting factory…</h1>
  </main>
  <main v-else-if="!session.user" class="auth-screen">
    <div class="panel">
      <h1>Welcome to legos-factory.</h1>
      <p>Sign in to your workspace to continue.</p>
      <a class="btn primary" href="/api/v1/auth/oidc/start">Sign in</a
      ><button class="btn" @click="session.bootstrap()">Try again</button>
    </div>
  </main>
  <RouterView v-else />
</template>
