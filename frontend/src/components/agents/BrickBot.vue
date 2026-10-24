<script setup lang="ts">
defineProps<{
  persona: "foreman" | "architect" | "builder" | "inspector" | "cataloger";
  size?: number;
}>();
</script>
<template>
  <svg
    class="bot agent-avatar"
    viewBox="0 0 70 88"
    :style="
      size ? { width: size + 'px', height: (size * 88) / 70 + 'px' } : undefined
    "
    role="img"
    :aria-label="`Brick character: ${persona}`"
  >
    <g v-if="persona === 'foreman'">
      <path d="M24 72v12h10V72m2 0v12h10V72" fill="#CB7F42" />
      <rect x="22" y="68" width="26" height="7" rx="2" fill="#CB7F42" />
      <path d="m20 49-8 19 7 3 6-17m25-5 8 19-7 3-6-17" fill="#CB7F42" />
      <path d="M23 48h24l3 24H20z" fill="#F2AA6B" />
      <path d="M28 49v14h14V49" fill="none" stroke="#fff7" stroke-width="2" />
      <rect x="30" y="60" width="10" height="7" rx="1.5" fill="#CB7F42" />
      <rect x="29" y="16" width="12" height="8" rx="2" fill="#EBCF8F" />
      <rect x="20" y="25" width="30" height="24" rx="7" fill="#F1D8A4" />
      <path d="M17 30c0-13 5-20 18-20s18 7 18 20" fill="#F7C65E" />
      <path d="M14 29h42v5H14z" fill="#DFA83F" />
      <path d="M32 9h6v19h-6z" fill="#FFD882" />
      <circle cx="29" cy="36" r="1.7" fill="#444154" />
      <circle cx="41" cy="36" r="1.7" fill="#444154" />
      <path
        d="M31 42q4 3 8 0"
        fill="none"
        stroke="#655344"
        stroke-width="1.3"
        stroke-linecap="round"
      />
      <path
        d="M24 54l5 7m-6-1 6-7m12 1 6 7m-6-1 6-7"
        stroke="#fff6"
        stroke-width="2"
      />
    </g>
    <g v-else-if="persona === 'architect'">
      <path d="M24 72v12h10V72m2 0v12h10V72" fill="#7C69CB" />
      <rect x="22" y="68" width="26" height="7" rx="2" fill="#7C69CB" />
      <path d="m20 49-8 19 7 3 6-17m25-5 8 19-7 3-6-17" fill="#7C69CB" />
      <path d="M23 48h24l3 24H20z" fill="#A89CEC" />
      <path d="M28 49v14h14V49" fill="none" stroke="#fff7" stroke-width="2" />
      <rect x="30" y="60" width="10" height="7" rx="1.5" fill="#7C69CB" />
      <rect x="29" y="16" width="12" height="8" rx="2" fill="#EBCF8F" />
      <rect x="20" y="25" width="30" height="24" rx="7" fill="#F1D8A4" />
      <path d="M17 28c-1-12 27-22 36-6l-2 7z" fill="#56518C" />
      <path d="m35 14 3-6" stroke="#56518C" stroke-width="3" />
      <path d="M16 28h36v5H16z" fill="#7065A0" />
      <circle cx="29" cy="36" r="1.7" fill="#444154" />
      <circle cx="41" cy="36" r="1.7" fill="#444154" />
      <path
        d="M31 42q4 3 8 0"
        fill="none"
        stroke="#655344"
        stroke-width="1.3"
        stroke-linecap="round"
      />
    </g>
    <g v-else-if="persona === 'builder'">
      <path d="M24 72v12h10V72m2 0v12h10V72" fill="#5794A6" />
      <rect x="22" y="68" width="26" height="7" rx="2" fill="#5794A6" />
      <path d="m20 49-8 19 7 3 6-17m25-5 8 19-7 3-6-17" fill="#5794A6" />
      <path d="M23 48h24l3 24H20z" fill="#8BBCC9" />
      <path d="M28 49v14h14V49" fill="none" stroke="#fff7" stroke-width="2" />
      <rect x="30" y="60" width="10" height="7" rx="1.5" fill="#5794A6" />
      <rect x="29" y="16" width="12" height="8" rx="2" fill="#EBCF8F" />
      <rect x="20" y="25" width="30" height="24" rx="7" fill="#F1D8A4" />
      <path d="M18 28c0-13 32-17 34 0z" fill="#457F9D" />
      <path d="M17 27h42v6H17z" fill="#356A87" />
      <circle cx="29" cy="36" r="1.7" fill="#444154" />
      <circle cx="41" cy="36" r="1.7" fill="#444154" />
      <path
        d="M31 42q4 3 8 0"
        fill="none"
        stroke="#655344"
        stroke-width="1.3"
        stroke-linecap="round"
      />
    </g>
    <g v-else-if="persona === 'inspector'">
      <path d="M24 72v12h10V72m2 0v12h10V72" fill="#6DA58A" />
      <rect x="22" y="68" width="26" height="7" rx="2" fill="#6DA58A" />
      <path d="m20 49-8 19 7 3 6-17m25-5 8 19-7 3-6-17" fill="#6DA58A" />
      <path d="M23 48h24l3 24H20z" fill="#A0CFB7" />
      <path d="M28 49v14h14V49" fill="none" stroke="#fff7" stroke-width="2" />
      <rect x="30" y="60" width="10" height="7" rx="1.5" fill="#6DA58A" />
      <rect x="29" y="16" width="12" height="8" rx="2" fill="#EBCF8F" />
      <rect x="20" y="25" width="30" height="24" rx="7" fill="#F1D8A4" />
      <path d="M19 29c0-18 30-18 32 0l-8-5-10 2-8-3z" fill="#3D6659" />
      <circle cx="29" cy="36" r="1.7" fill="#444154" />
      <circle cx="41" cy="36" r="1.7" fill="#444154" />
      <path
        d="M31 42q4 3 8 0"
        fill="none"
        stroke="#655344"
        stroke-width="1.3"
        stroke-linecap="round"
      />
      <g fill="none" stroke="#484C61" stroke-width="1.4">
        <rect x="23" y="32" width="10" height="8" rx="2" />
        <rect x="37" y="32" width="10" height="8" rx="2" />
        <path d="M33 35h4" />
      </g>
    </g>
    <g v-else-if="persona === 'cataloger'">
      <path d="M24 72v12h10V72m2 0v12h10V72" fill="#AE789A" />
      <rect x="22" y="68" width="26" height="7" rx="2" fill="#AE789A" />
      <path d="m20 49-8 19 7 3 6-17m25-5 8 19-7 3-6-17" fill="#AE789A" />
      <path d="M23 48h24l3 24H20z" fill="#D7A8C5" />
      <path d="M28 49v14h14V49" fill="none" stroke="#fff7" stroke-width="2" />
      <rect x="30" y="60" width="10" height="7" rx="1.5" fill="#AE789A" />
      <rect x="29" y="16" width="12" height="8" rx="2" fill="#EBCF8F" />
      <rect x="20" y="25" width="30" height="24" rx="7" fill="#F1D8A4" />
      <path d="M18 29c0-16 33-18 34 1l-9-4-9 2-8-5z" fill="#87637F" />
      <circle cx="29" cy="36" r="1.7" fill="#444154" />
      <circle cx="41" cy="36" r="1.7" fill="#444154" />
      <path
        d="M31 42q4 3 8 0"
        fill="none"
        stroke="#655344"
        stroke-width="1.3"
        stroke-linecap="round"
      />
      <g fill="none" stroke="#484C61" stroke-width="1.4">
        <rect x="23" y="32" width="10" height="8" rx="2" />
        <rect x="37" y="32" width="10" height="8" rx="2" />
        <path d="M33 35h4" />
      </g>
      <path d="M43 57h12v15H43z" fill="#FFF4DC" />
      <path d="M45 61h7m-7 4h7" stroke="#AE789A" />
    </g>
  </svg>
</template>
