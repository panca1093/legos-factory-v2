<script setup lang="ts">
import { computed } from "vue";
import Button from "../../components/ui/Button.vue";
import type {
  ChatPermission,
  ChatPermissionDecision,
} from "../../types/api";

const props = defineProps<{ permission: ChatPermission }>();
defineEmits<{ answer: [decision: ChatPermissionDecision] }>();

const pretty = computed(() => {
  try {
    return JSON.stringify(JSON.parse(props.permission.input), null, 2);
  } catch {
    return props.permission.input;
  }
});
const outcome = computed(
  () =>
    ({
      allowed: "Allowed",
      denied: "Denied",
      expired: "Expired without an answer, so it was refused",
    })[props.permission.state as string],
);
const locked = computed(() => props.permission.state !== "pending");
</script>
<template>
  <div
    class="ch-perm"
    :class="permission.state"
    role="group"
    :aria-label="`Permission for ${permission.server} ${permission.tool}`"
    data-test="permission-card"
  >
    <p class="ch-perm-title">
      <strong>{{ permission.server }} · {{ permission.tool }}</strong> wants to
      run
    </p>
    <details v-if="permission.input">
      <summary>Input</summary>
      <pre data-test="permission-input">{{ pretty }}</pre>
    </details>
    <p v-if="outcome" class="ch-perm-outcome" data-test="permission-outcome">
      {{ outcome }}
    </p>
    <div v-else class="ch-perm-actions">
      <Button
        small
        variant="primary"
        :disabled="locked"
        data-test="permission-allow"
        @click="$emit('answer', 'allow')"
        >Izinkan</Button
      >
      <Button
        small
        :disabled="locked"
        data-test="permission-allow-all"
        @click="$emit('answer', 'allow_all')"
        >Izinkan semua di chat ini</Button
      >
      <Button
        small
        variant="ghost"
        :disabled="locked"
        data-test="permission-deny"
        @click="$emit('answer', 'deny')"
        >Tolak</Button
      >
    </div>
  </div>
</template>
