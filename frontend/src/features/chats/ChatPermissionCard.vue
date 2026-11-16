<script setup lang="ts">
import { computed } from "vue";
import Button from "../../components/ui/Button.vue";
import type { ChatPermission, ChatPermissionDecision } from "../../types/api";

const props = defineProps<{ permission: ChatPermission }>();
defineEmits<{ answer: [decision: ChatPermissionDecision] }>();

const parsed = computed<unknown>(() => {
  try {
    return JSON.parse(props.permission.input);
  } catch {
    return undefined;
  }
});
const pretty = computed(() =>
  parsed.value === undefined
    ? props.permission.input
    : JSON.stringify(parsed.value, null, 2),
);
const toolLabel = computed(() => {
  if (props.permission.tool !== "execute_tools") return props.permission.tool;
  const ex = (parsed.value as { executions?: unknown } | undefined)?.executions;
  const names = Array.isArray(ex)
    ? ex.flatMap((e) =>
        typeof e?.tool === "string" && e.tool ? [e.tool as string] : [],
      )
    : [];
  return names.length
    ? `${props.permission.tool}: ${names.join(", ")}`
    : props.permission.tool;
});
const decided = computed(() =>
  ["allowed", "denied", "expired"].includes(props.permission.state),
);
const outcome = computed(
  () =>
    ({
      denied: "denied",
      expired: "expired, refused",
    })[props.permission.state as string],
);
const locked = computed(() => props.permission.state !== "pending");
</script>
<template>
  <details
    v-if="decided"
    class="ch-perm-line"
    :class="permission.state"
    data-test="permission-card"
  >
    <summary>
      <span class="ch-perm-mark" aria-hidden="true">{{
        permission.state === "allowed" ? "✓" : "✗"
      }}</span>
      <span data-test="permission-label"
        >{{ permission.server }} · {{ toolLabel }}</span
      >
      <span
        v-if="outcome"
        class="ch-perm-outcome"
        data-test="permission-outcome"
        >{{ outcome }}</span
      >
    </summary>
    <pre v-if="permission.input" data-test="permission-input">{{ pretty }}</pre>
  </details>
  <div
    v-else
    class="ch-perm"
    :class="permission.state"
    role="group"
    :aria-label="`Permission for ${permission.server} ${toolLabel}`"
    data-test="permission-card"
  >
    <p class="ch-perm-title">
      <strong data-test="permission-label"
        >{{ permission.server }} · {{ toolLabel }}</strong
      >
      wants to run
    </p>
    <details v-if="permission.input">
      <summary>Input</summary>
      <pre data-test="permission-input">{{ pretty }}</pre>
    </details>
    <div class="ch-perm-actions">
      <Button
        small
        variant="primary"
        :disabled="locked"
        data-test="permission-allow"
        @click="$emit('answer', 'allow')"
        >Allow</Button
      >
      <Button
        small
        :disabled="locked"
        data-test="permission-allow-all"
        @click="$emit('answer', 'allow_all')"
        >Allow all in this chat</Button
      >
      <Button
        small
        variant="ghost"
        :disabled="locked"
        data-test="permission-deny"
        @click="$emit('answer', 'deny')"
        >Deny</Button
      >
    </div>
  </div>
</template>
