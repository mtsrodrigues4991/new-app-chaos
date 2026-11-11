# Imagem do itau-case-pix para o KIND: compila com o Maven da imagem (sem o mvnw, que quebra
# com fim de linha do Windows) e roda só o jar numa JRE.
# Contexto do build: a pasta do repositório itau-case-pix.
FROM maven:3.9-eclipse-temurin-17 AS build
WORKDIR /app
COPY pom.xml .
RUN mvn -B -q dependency:go-offline
COPY src ./src
RUN mvn -B -q package -DskipTests \
 && cp "$(ls target/*.jar | grep -v '\.original$' | head -1)" /app.jar

FROM eclipse-temurin:17-jre-alpine
RUN addgroup -S app && adduser -S app -G app
USER app
COPY --from=build /app.jar /app/app.jar
EXPOSE 8080
ENTRYPOINT ["sh", "-c", "exec java $JAVA_OPTS -jar /app/app.jar"]
