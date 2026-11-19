<script setup lang="ts">
import { computed } from "vue";
import { useRoute } from "vue-router";

const route = useRoute();
const code = computed(() => route.name === "project-code");
</script>
<template>
  <div class="panel empty pw-soon" data-test="placeholder">
    <h2>{{ code ? "Code is coming" : "Pull requests are coming" }}</h2>
    <p class="help">
      {{
        code
          ? "Open and edit this project's files here once the editor lands."
          : "Review the pull requests you are involved in, right next to the work orders that opened them."
      }}
    </p>
  </div>
</template>
