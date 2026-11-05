<script setup lang="ts">
import { computed } from "vue";
import BrickBot from "../../components/agents/BrickBot.vue";
import { isCrew, persona } from "../../composables/presentation";
const props = defineProps<{ actor: string; small?: boolean }>();
const kind = computed(() =>
  props.actor === "factory"
    ? "code"
    : props.actor === "jev"
      ? "jev"
      : props.actor === "you"
        ? "human"
        : isCrew(props.actor)
          ? "agent"
          : "code",
);
const label = computed(
  () =>
    ({ code: "Code", jev: "Jev", human: "You" })[
      kind.value as "code" | "jev" | "human"
    ] ?? props.actor,
);
</script>
<template>
  <span
    class="fx-ab"
    :class="[kind, kind === 'agent' && actor.toLowerCase(), { small }]"
    :title="kind === 'agent' ? actor : label"
  >
    <BrickBot v-if="kind === 'agent'" :persona="persona(actor)" />
    <template v-else-if="kind === 'code'">{ }</template>
    <template v-else-if="kind === 'jev'">J</template>
    <template v-else>You</template>
  </span>
</template>
